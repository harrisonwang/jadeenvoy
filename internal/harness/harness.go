// Package harness 是 agent 主循环：
// 拼 prompt → 调 LLM → 派发 tool → 回写事件 → 循环。
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/harrisonwang/jadeenvoy/internal/event"
	"github.com/harrisonwang/jadeenvoy/internal/memory"
	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/tool"
)

// Harness 用注入的 deps 跑 agent loop。
type Harness struct {
	Store    *store.Store
	Broker   *event.Broker
	Provider provider.Provider
	Sandbox  sandbox.Provider
	Tools    *tool.Registry
	Memory   *memory.Service // 可空（V1 没 memory）
	MaxSteps int             // 每个 turn 最多多少轮 LLM 调用

	// Vault MITM 注入（可空）：设了 VaultProxyURL 就给沙箱注入 HTTPS_PROXY + CA 信任。
	VaultProxyURL string
	VaultCACert   string

	turnMu sync.Map // sessionID -> *sync.Mutex，防同一 session 并发 RunTurn
}

func New(st *store.Store, br *event.Broker, p provider.Provider, sb sandbox.Provider, tr *tool.Registry) *Harness {
	return &Harness{
		Store:    st,
		Broker:   br,
		Provider: p,
		Sandbox:  sb,
		Tools:    tr,
		MaxSteps: 20,
	}
}

func (h *Harness) lockTurn(sessionID string) func() {
	v, _ := h.turnMu.LoadOrStore(sessionID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return func() { mu.Unlock() }
}

// RunTurn 处理一个 user.message 事件之后的整个 turn。
// 注意：这是同步阻塞的。生产里 server 应该 goroutine 调它。
func (h *Harness) RunTurn(ctx context.Context, sessionID string) error {
	unlock := h.lockTurn(sessionID)
	defer unlock()

	log := obs.Logger().With("session_id", sessionID)
	log.Info("harness.turn.start")

	// 拿 session
	sess, err := h.Store.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	// 解析 agent snapshot
	var agentCfg struct {
		Model  json.RawMessage `json:"model"`
		System string          `json:"system"`
		Tools  json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(sess.AgentSnapshot, &agentCfg)
	modelID := parseModelID(agentCfg.Model)

	// 从 agent snapshot 中解析本 turn 的自定义工具定义。不要写入全局 tool
	// registry，否则并发 session 会互相覆盖 custom tools。
	customDefs := parseCustomToolDefs(agentCfg.Tools)
	customToolNames := map[string]struct{}{}
	for _, d := range customDefs {
		customToolNames[d.Name] = struct{}{}
	}
	isCustomTool := func(name string) bool {
		_, ok := customToolNames[name]
		return ok
	}

	// provision sandbox（每 session 一次；这里简化每 turn 都 provision，V1 OK）
	sb, err := h.Sandbox.Provision(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("provision sandbox: %w", err)
	}
	defer sb.Close()

	// Vault MITM：给沙箱出站注入 HTTPS_PROXY（session 编进 proxy userinfo）+ CA 信任。
	h.injectVaultProxy(sb, sessionID)

	// 挂载 agent skills 到沙箱，注入 SKILL.md 到 system prompt
	skillExtras, err := h.mountSkills(ctx, sb, sess.AgentSnapshot)
	if err != nil {
		log.Warn("harness.skills.mount.failed", "err", err.Error())
	}
	if skillExtras != "" {
		agentCfg.System += skillExtras
	}

	// 挂载 session resources（M2 仅 memory_store）
	systemPromptExtras, err := h.mountResources(ctx, sb, sessionID)
	if err != nil {
		log.Warn("harness.mount.failed", "err", err.Error())
	}
	if systemPromptExtras != "" {
		agentCfg.System += systemPromptExtras
	}

	// 转 running
	_ = h.Store.UpdateSessionStatus(ctx, sessionID, "running")
	_, _ = h.Broker.Publish(ctx, sessionID, "session.status_running", "primary", json.RawMessage(`{"type":"session.status_running"}`))

	// 循环
	for step := 0; step < h.MaxSteps; step++ {
		// 拼 messages from event log
		messages, err := h.buildMessages(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("build messages: %w", err)
		}

		// 发 span.model_request_start
		startPayload, _ := json.Marshal(map[string]any{
			"type":  "span.model_request_start",
			"model": modelID,
			"step":  step,
		})
		_, _ = h.Broker.Publish(ctx, sessionID, "span.model_request_start", "primary", startPayload)

		// 调 provider
		req := provider.ChatRequest{
			Model:     modelID,
			System:    agentCfg.System,
			Messages:  messages,
			Tools:     toProviderTools(append(h.Tools.BuiltinDefs(), customDefs...)),
			MaxTokens: 4096,
		}
		ch, err := h.Provider.Stream(ctx, req)
		if err != nil {
			return fmt.Errorf("provider stream: %w", err)
		}

		// 累积流
		var textBuf strings.Builder
		var pendingTools []pendingToolUse
		var usage provider.Usage
		stopReason := ""

	streamLoop:
		for ev := range ch {
			switch e := ev.(type) {
			case provider.TextDelta:
				textBuf.WriteString(e.Text)
			case provider.ToolUseStart:
				pendingTools = append(pendingTools, pendingToolUse{
					ID:   e.ID,
					Name: e.Name,
				})
			case provider.ToolUseDelta:
				if len(pendingTools) > 0 {
					pendingTools[len(pendingTools)-1].InputJSON += e.InputJSON
				}
			case provider.StopReason:
				stopReason = e.Reason
				usage = e.Usage
				break streamLoop
			case provider.ErrorEvent:
				return fmt.Errorf("provider error: %s — %s", e.Type, e.Message)
			}
		}

		// 写 span.model_request_end
		endPayload, _ := json.Marshal(map[string]any{
			"type":          "span.model_request_end",
			"model":         modelID,
			"step":          step,
			"finish_reason": stopReason,
			"model_usage": map[string]any{
				"input_tokens":                usage.InputTokens,
				"output_tokens":               usage.OutputTokens,
				"cache_creation_input_tokens": usage.CacheCreationInputTokens,
				"cache_read_input_tokens":     usage.CacheReadInputTokens,
			},
		})
		_, _ = h.Broker.Publish(ctx, sessionID, "span.model_request_end", "primary", endPayload)

		// usage 累加
		_ = h.Store.UpdateSessionUsage(ctx, sessionID, usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)

		// 如果有文本，写 agent.message
		if textBuf.Len() > 0 {
			content := []map[string]any{{"type": "text", "text": textBuf.String()}}
			payload, _ := json.Marshal(map[string]any{
				"type":    "agent.message",
				"content": content,
			})
			_, _ = h.Broker.Publish(ctx, sessionID, "agent.message", "primary", payload)
		}

		// 如果有 tool_use，执行 + 回写 tool_result
		if len(pendingTools) > 0 {
			for _, pt := range pendingTools {
				// 确定事件类型：自定义工具用 agent.custom_tool_use，内置用 agent.tool_use
				toolUseEvType := "agent.tool_use"
				if isCustomTool(pt.Name) {
					toolUseEvType = "agent.custom_tool_use"
				}
				// 写 tool_use 事件
				input := json.RawMessage(pt.InputJSON)
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				toolUsePayload, _ := json.Marshal(map[string]any{
					"type":  toolUseEvType,
					"id":    pt.ID,
					"name":  pt.Name,
					"input": input,
				})
				_, _ = h.Broker.Publish(ctx, sessionID, toolUseEvType, "primary", toolUsePayload)

				// 自定义工具：不进内置工具执行，暂停 loop 等 client 操作
				if isCustomTool(pt.Name) {
					// 更新状态为 requires_action
					_ = h.Store.UpdateSessionStatus(ctx, sessionID, "requires_action")
					sess, _ = h.Store.GetSession(ctx, sessionID) // 刷新

					raPayload, _ := json.Marshal(map[string]any{
						"type":             "session.status_requires_action",
						"agent_session_id": sessionID,
						"required_actions": []map[string]any{
							{
								"type": "custom_tool_use",
								"custom_tool_use": map[string]any{
									"id":    pt.ID,
									"name":  pt.Name,
									"input": json.RawMessage(pt.InputJSON),
								},
							},
						},
					})
					_, _ = h.Broker.Publish(ctx, sessionID, "session.status_requires_action", "primary", raPayload)

					log.Info("harness.turn.requires_action", "tool", pt.Name)
					return nil // 停止 loop，等 client 发 custom_tool_result
				}

				// 执行内置工具
				t, ok := h.Tools.Get(pt.Name)
				if !ok {
					errMsg := fmt.Sprintf("unknown tool: %s", pt.Name)
					resPayload, _ := json.Marshal(map[string]any{
						"type":        "agent.tool_result",
						"tool_use_id": pt.ID,
						"content":     errMsg,
						"is_error":    true,
					})
					_, _ = h.Broker.Publish(ctx, sessionID, "agent.tool_result", "primary", resPayload)
					continue
				}
				res, _ := t.Execute(ctx, sb, input)
				resPayload, _ := json.Marshal(map[string]any{
					"type":        "agent.tool_result",
					"tool_use_id": pt.ID,
					"content":     res.Content,
					"is_error":    res.IsError,
				})
				_, _ = h.Broker.Publish(ctx, sessionID, "agent.tool_result", "primary", resPayload)
			}
			// 工具执行完，继续下一轮 LLM 调用
			continue
		}

		// 没有 tool_use → 这是 end_turn
		log.Info("harness.turn.end", "reason", stopReason, "steps", step+1)
		break
	}

	// 转 idle
	_ = h.Store.UpdateSessionStatus(ctx, sessionID, "idle")
	idlePayload, _ := json.Marshal(map[string]any{
		"type":        "session.status_idle",
		"stop_reason": map[string]string{"type": "end_turn"},
	})
	_, _ = h.Broker.Publish(ctx, sessionID, "session.status_idle", "primary", idlePayload)

	return nil
}

// injectVaultProxy 给支持 SetEnv 的沙箱注入 vault MITM 代理 env。
// session id 编进 proxy userinfo（HTTPS_PROXY=http://<sessionID>:x@host），
// 代理据此查 session 的 vault 凭据 —— 无需改写沙箱里的每条 curl。
func (h *Harness) injectVaultProxy(sb sandbox.Sandbox, sessionID string) {
	if h.VaultProxyURL == "" {
		return
	}
	proxyURL := h.VaultProxyURL
	if u, err := url.Parse(h.VaultProxyURL); err == nil {
		u.User = url.UserPassword(sessionID, "x")
		proxyURL = u.String()
	} else {
		// 解析失败则注入无 session 标识的代理 URL，代理将无法识别 session —— 显式告警
		obs.Logger().Warn("harness.vault_proxy.parse_failed", "url", h.VaultProxyURL, "err", err.Error())
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		sb.SetEnv(k, proxyURL)
	}
	if h.VaultCACert != "" {
		for _, k := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"} {
			sb.SetEnv(k, h.VaultCACert)
		}
	}
}
