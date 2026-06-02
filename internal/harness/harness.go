// Package harness 是 agent 主循环：
// 拼 prompt → 调 LLM → 派发 tool → 回写事件 → 循环。
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/event"
	"github.com/harrisonwang/jadeenvoy/internal/memory"
	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/tool"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
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

	// Context compaction（ADR-0021）。CompactThresholdTokens<=0 关闭。
	CompactThresholdTokens int
	KeepRecentTurns        int

	// 错误恢复（ADR-0022）。瞬时错误重试 MaxRetries 次，每次退避 RetryBackoff*attempt。
	MaxRetries   int
	RetryBackoff time.Duration

	// Vault MITM 注入（可空）：设了 VaultProxyURL 就给沙箱注入 HTTPS_PROXY + CA 信任。
	VaultProxyURL string
	VaultCACert   string

	// VaultResolveToken（可空）：按 (tenantID, vaultIDs, host) 返回 static_bearer token，
	// 供 MCP 鉴权注入（ADR-0026）。由 jed 用 vault.Service.Resolve 包成闭包注入，
	// 避免 harness 直接依赖 vault 包。
	VaultResolveToken func(ctx context.Context, tenantID string, vaultIDs []string, host string) string

	turnMu  sync.Map // sessionID -> *sync.Mutex，防同一 session 并发 RunTurn
	cancels sync.Map // sessionID -> context.CancelFunc，供 Interrupt 取消运行中 turn
}

// Interrupt 取消某 session 正在运行的 turn（ADR-0025）。返回是否有 turn 被取消。
func (h *Harness) Interrupt(sessionID string) bool {
	if v, ok := h.cancels.Load(sessionID); ok {
		v.(context.CancelFunc)()
		return true
	}
	return false
}

func New(st *store.Store, br *event.Broker, p provider.Provider, sb sandbox.Provider, tr *tool.Registry) *Harness {
	return &Harness{
		Store:        st,
		Broker:       br,
		Provider:     p,
		Sandbox:      sb,
		Tools:        tr,
		MaxSteps:     20,
		MaxRetries:   2,
		RetryBackoff: 0, // 默认 0：测试不睡；jed 注入生产值
	}
}

// DestroySandbox 回收 session 的沙箱资源（session 删除时调用）。
func (h *Harness) DestroySandbox(ctx context.Context, sessionID string) error {
	return h.Sandbox.Destroy(ctx, sessionID)
}

func (h *Harness) lockTurn(sessionID string) func() {
	v, _ := h.turnMu.LoadOrStore(sessionID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return func() { mu.Unlock() }
}

// RunTurn 处理一个 user.message 事件之后的整个 turn。
// 注意：这是同步阻塞的。生产里 server 应该 goroutine 调它。
//
// 命名返回值 retErr 配合 defer 兜底：任何未处理错误都会被收敛成
// session.status_terminated，绝不把 session 留在 running 死锁（ADR-0022）。
func (h *Harness) RunTurn(ctx context.Context, sessionID string) (retErr error) {
	return h.RunThread(ctx, sessionID, "primary")
}

// RunThread 处理指定 session_thread_id 的一个 turn。primary 线程保持原有 RunTurn 语义；
// 非 primary 线程用于 multi-agent / handoff 场景下的独立上下文。
func (h *Harness) RunThread(ctx context.Context, sessionID, threadID string) (retErr error) {
	if threadID == "" {
		threadID = "primary"
	}
	unlock := h.lockTurn(sessionID)
	defer unlock()

	log := obs.Logger().With("session_id", sessionID, "session_thread_id", threadID)
	log.Info("harness.turn.start")

	// 可取消 context：注册 cancel 供 Interrupt 调用（ADR-0025）。
	ctx, cancel := context.WithCancel(ctx)
	h.cancels.Store(sessionID, cancel)
	defer func() {
		cancel()
		h.cancels.Delete(sessionID)
	}()

	// 终态兜底（用 background ctx，防 ctx 已取消）：
	//   - context.Canceled = 用户 interrupt → clean idle{stop_reason:interrupt}，不是错误（ADR-0025）
	//   - 其他错误 → session.status_terminated（ADR-0022）
	defer func() {
		if retErr == nil {
			return
		}
		bg := context.Background()
		if errors.Is(retErr, context.Canceled) {
			log.Info("harness.turn.interrupted")
			_ = h.Store.UpdateSessionStatus(bg, sessionID, "idle")
			payload, _ := json.Marshal(map[string]any{
				"type":        "session.status_idle",
				"stop_reason": map[string]string{"type": "interrupt"},
			})
			_, _ = h.Broker.Publish(bg, sessionID, "session.status_idle", threadID, payload)
			retErr = nil // 中断不是错误，吞掉
			return
		}
		log.Error("harness.turn.error", "err", retErr.Error())
		_ = h.Store.MarkSessionTerminated(bg, sessionID)
		payload, _ := json.Marshal(map[string]any{
			"type": "session.status_terminated",
			"error": map[string]string{
				"type":    "agent_error",
				"message": retErr.Error(),
			},
		})
		_, _ = h.Broker.Publish(bg, sessionID, "session.status_terminated", threadID, payload)
	}()

	// 拿 session
	sess, err := h.Store.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	// 解析 agent snapshot
	var agentCfg struct {
		Model      json.RawMessage           `json:"model"`
		System     string                    `json:"system"`
		Tools      json.RawMessage           `json:"tools"`
		Guardrails *apitypes.AgentGuardrails `json:"guardrails"`
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
	permissions := newPermissionDecider(agentCfg.Tools)

	// provision sandbox（每 session 一次；这里简化每 turn 都 provision，V1 OK）
	sb, err := h.Sandbox.Provision(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("provision sandbox: %w", err)
	}
	defer sb.Close()

	// Vault MITM：给沙箱出站注入 HTTPS_PROXY（session 编进 proxy userinfo）+ CA 信任。
	h.injectVaultProxy(sb, sessionID)

	// 连接 agent 声明的 MCP server，发现工具（无鉴权，ADR-0024）。单个失败仅告警跳过。
	var resolveAuth credResolver
	if h.VaultResolveToken != nil {
		var vaultIDs []string
		_ = json.Unmarshal(sess.VaultIDs, &vaultIDs)
		tenantID := sess.TenantID
		resolveAuth = func(host string) string {
			return h.VaultResolveToken(ctx, tenantID, vaultIDs, host)
		}
	}
	mcpSess := connectMCP(ctx, sess.AgentSnapshot, resolveAuth)

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
	_, _ = h.Broker.Publish(ctx, sessionID, "session.status_running", threadID, json.RawMessage(`{"type":"session.status_running"}`))

	// 历史超预算则先压缩（ADR-0021）：在 turn 边界摘要旧历史成 checkpoint 事件。
	if err := h.maybeCompact(ctx, sessionID, threadID, modelID); err != nil {
		log.Warn("harness.compact.failed", "err", err.Error())
	}
	if err := h.resolvePendingToolConfirmations(ctx, sessionID, threadID, sb, mcpSess); err != nil {
		return fmt.Errorf("resolve tool confirmations: %w", err)
	}

	// 循环
	hitMaxSteps := true
	finalStop := "end_turn"
	for step := 0; step < h.MaxSteps; step++ {
		// 每步先查中断（ADR-0025）：被 Interrupt 取消则尽快返回 Canceled，
		// 由 defer 收敛成 clean idle{interrupt}。
		if err := ctx.Err(); err != nil {
			return err
		}
		// 拼 messages from event log
		messages, err := h.buildMessages(ctx, sessionID, threadID)
		if err != nil {
			return fmt.Errorf("build messages: %w", err)
		}

		// 发 span.model_request_start
		startPayload, _ := json.Marshal(map[string]any{
			"type":  "span.model_request_start",
			"model": modelID,
			"step":  step,
		})
		_, _ = h.Broker.Publish(ctx, sessionID, "span.model_request_start", threadID, startPayload)

		// 调 provider（带瞬时错误重试，见 ADR-0022）
		llmTools := toProviderTools(append(h.Tools.BuiltinDefs(), customDefs...))
		llmTools = append(llmTools, mcpSess.providerToolDefs()...)
		req := provider.ChatRequest{
			Model:     modelID,
			System:    agentCfg.System,
			Messages:  messages,
			Tools:     llmTools,
			MaxTokens: 4096,
		}
		res, err := h.callModelWithRetry(ctx, sessionID, req)
		if err != nil {
			return err // defer 兜底成 terminated
		}
		textBuf := res.text
		pendingTools := res.tools
		usage := res.usage
		stopReason := res.stopReason

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
		_, _ = h.Broker.Publish(ctx, sessionID, "span.model_request_end", threadID, endPayload)

		// usage 累加
		_ = h.Store.UpdateSessionUsage(ctx, sessionID, usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)

		// 如果有文本，写 agent.message
		if textBuf != "" {
			content := []map[string]any{{"type": "text", "text": textBuf}}
			payload, _ := json.Marshal(map[string]any{
				"type":    "agent.message",
				"content": content,
			})
			_, _ = h.Broker.Publish(ctx, sessionID, "agent.message", threadID, payload)
		}

		// 如果有 tool_use，执行 + 回写 tool_result
		if len(pendingTools) > 0 {
			for _, pt := range pendingTools {
				// 确定事件类型：MCP 用 agent.mcp_tool_use，自定义用 agent.custom_tool_use，内置用 agent.tool_use
				toolUseEvType := "agent.tool_use"
				switch {
				case mcpSess.isMCPTool(pt.Name):
					toolUseEvType = "agent.mcp_tool_use"
				case isCustomTool(pt.Name):
					toolUseEvType = "agent.custom_tool_use"
				}
				// 写 tool_use 事件
				input := json.RawMessage(pt.InputJSON)
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				requiresConfirmation := permissions.requiresApproval(pt.Name, mcpSess.isMCPTool(pt.Name), isCustomTool(pt.Name))
				toolUseBody := map[string]any{
					"type":  toolUseEvType,
					"id":    pt.ID,
					"name":  pt.Name,
					"input": input,
				}
				if requiresConfirmation {
					toolUseBody["requires_confirmation"] = true
				}
				toolUsePayload, _ := json.Marshal(toolUseBody)
				toolUseEv, err := h.Broker.Publish(ctx, sessionID, toolUseEvType, threadID, toolUsePayload)
				if err != nil {
					return err
				}

				if ok, reason := toolAllowedByGuardrails(agentCfg.Guardrails, pt.Name); !ok {
					violationPayload, _ := json.Marshal(map[string]any{
						"type":        "agent.guardrail_violation",
						"tool_use_id": pt.ID,
						"name":        pt.Name,
						"reason":      reason,
					})
					_, _ = h.Broker.Publish(ctx, sessionID, "agent.guardrail_violation", threadID, violationPayload)

					resultType := "agent.tool_result"
					if mcpSess.isMCPTool(pt.Name) {
						resultType = "agent.mcp_tool_result"
					}
					h.publishToolResult(ctx, sessionID, threadID, resultType, pt.ID, toolUseEv.ID, reason, true)
					continue
				}

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
					_, _ = h.Broker.Publish(ctx, sessionID, "session.status_requires_action", threadID, raPayload)

					log.Info("harness.turn.requires_action", "tool", pt.Name)
					return nil // 停止 loop，等 client 发 custom_tool_result
				}

				if requiresConfirmation {
					_ = h.Store.UpdateSessionStatus(ctx, sessionID, "idle")
					_, _ = h.Broker.Publish(ctx, sessionID, "session.status_idle", threadID, requiresActionIdlePayload([]string{toolUseEv.ID}))
					log.Info("harness.turn.awaiting_tool_confirmation", "tool", pt.Name, "tool_use_event_id", toolUseEv.ID)
					return nil
				}

				resultType := toolResultEventType(toolUseEvType)
				content, isErr := h.executeServerTool(ctx, sb, mcpSess, pt.Name, input)
				h.publishToolResult(ctx, sessionID, threadID, resultType, pt.ID, toolUseEv.ID, content, isErr)
			}
			// 工具执行完，继续下一轮 LLM 调用
			continue
		}

		// 没有 tool_use → 自然结束，用 provider 报告的真实 stop_reason
		hitMaxSteps = false
		finalStop = stopReason
		if finalStop == "" {
			finalStop = "end_turn"
		}
		log.Info("harness.turn.end", "reason", finalStop, "steps", step+1)
		break
	}
	// 跑满循环仍未自然结束 → 撞 MaxSteps 上限（不要谎报 end_turn）
	if hitMaxSteps {
		finalStop = "max_turns"
		log.Info("harness.turn.max_steps", "steps", h.MaxSteps)
	}

	// 转 idle，stop_reason 反映真实停止原因（end_turn / max_turns / max_tokens / ...）
	_ = h.Store.UpdateSessionStatus(ctx, sessionID, "idle")
	idlePayload, _ := json.Marshal(map[string]any{
		"type":        "session.status_idle",
		"stop_reason": map[string]string{"type": finalStop},
	})
	_, _ = h.Broker.Publish(ctx, sessionID, "session.status_idle", threadID, idlePayload)

	return nil
}

// modelResult 是一次完整 model 调用累积后的结果。
type modelResult struct {
	text       string
	tools      []pendingToolUse
	usage      provider.Usage
	stopReason string
}

// callModelWithRetry 调一次 model；瞬时错误（见 provider.APIError.Retryable）按
// MaxRetries 重试，重试前 publish session.status_rescheduled。永久错误立即返回（ADR-0022）。
func (h *Harness) callModelWithRetry(ctx context.Context, sessionID string, req provider.ChatRequest) (modelResult, error) {
	var lastErr error
	for attempt := 0; attempt <= h.MaxRetries; attempt++ {
		if attempt > 0 {
			// 通告进入 rescheduling，并退避。
			_ = h.Store.UpdateSessionStatus(ctx, sessionID, "rescheduling")
			reschedPayload, _ := json.Marshal(map[string]any{
				"type":    "session.status_rescheduled",
				"attempt": attempt,
				"error":   map[string]string{"message": lastErr.Error()},
			})
			_, _ = h.Broker.Publish(ctx, sessionID, "session.status_rescheduled", "primary", reschedPayload)
			if h.RetryBackoff > 0 {
				select {
				case <-ctx.Done():
					return modelResult{}, ctx.Err()
				case <-time.After(h.RetryBackoff * time.Duration(attempt)):
				}
			}
			_ = h.Store.UpdateSessionStatus(ctx, sessionID, "running")
		}

		res, err := h.callModelOnce(ctx, req)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !retryable(err) {
			return modelResult{}, err
		}
	}
	return modelResult{}, fmt.Errorf("model call failed after %d retries: %w", h.MaxRetries, lastErr)
}

// callModelOnce 调一次 provider 并把 stream 累积成 modelResult。流内 ErrorEvent 转成
// provider.APIError 返回，交由上层判断是否重试。
func (h *Harness) callModelOnce(ctx context.Context, req provider.ChatRequest) (modelResult, error) {
	ch, err := h.Provider.Stream(ctx, req)
	if err != nil {
		return modelResult{}, err
	}
	var out modelResult
	var textBuf strings.Builder
	for ev := range ch {
		switch e := ev.(type) {
		case provider.TextDelta:
			textBuf.WriteString(e.Text)
		case provider.ToolUseStart:
			out.tools = append(out.tools, pendingToolUse{ID: e.ID, Name: e.Name})
		case provider.ToolUseDelta:
			if len(out.tools) > 0 {
				out.tools[len(out.tools)-1].InputJSON += e.InputJSON
			}
		case provider.StopReason:
			out.stopReason = e.Reason
			out.usage = e.Usage
			out.text = textBuf.String()
			return out, nil
		case provider.ErrorEvent:
			return modelResult{}, e.APIError()
		}
	}
	// 流提前结束（无 StopReason）：当作可重试的 api_error。
	out.text = textBuf.String()
	if out.stopReason == "" {
		return modelResult{}, &provider.APIError{Type: "api_error", Message: "stream ended without stop_reason"}
	}
	return out, nil
}

// retryable 报告 err 是否为可重试的 provider.APIError。
func retryable(err error) bool {
	var apiErr *provider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	return false
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
