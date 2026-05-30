// Package e2e — JadeEnvoy V1 端到端测试。
//
// 这是 MVP 的验收测试：起一个内存中的 jed，create agent → create session →
// send user.message → 触发 mock LLM → 触发 bash 工具 → 等到 session.status_idle →
// 验证事件序列符合预期。
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/agent"
	"github.com/harrisonwang/jadeenvoy/internal/api"
	"github.com/harrisonwang/jadeenvoy/internal/event"
	"github.com/harrisonwang/jadeenvoy/internal/harness"
	"github.com/harrisonwang/jadeenvoy/internal/memory"
	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/session"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/tool"
	"github.com/harrisonwang/jadeenvoy/internal/webhook"
)

// setupHarness 起一个真实但内存隔离的 jed 栈，返回 httptest server + mock provider。
func setupHarness(t *testing.T) (*httptest.Server, *provider.MockProvider) {
	t.Helper()
	tmp := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	broker := event.NewBroker(st)
	mockProv := provider.NewMockProvider()
	sbProvider := sandbox.NewLocalSubprocessProvider(filepath.Join(tmp, "sandboxes"))

	registry := tool.NewRegistry()
	registry.Register(tool.BashTool{})
	registry.Register(tool.ReadTool{})
	registry.Register(tool.WriteTool{})
	registry.Register(tool.EditTool{})
	registry.Register(tool.GlobTool{})
	registry.Register(tool.GrepTool{})

	agentSvc := agent.New(st)
	sessionSvc := session.New(st)
	memorySvc := memory.New(st)
	webhookSvc := webhook.New(st)
	hrns := harness.New(st, broker, mockProv, sbProvider, registry)
	hrns.Memory = memorySvc

	// 接通 broker → webhook
	broker.RegisterHook(func(ev event.Event) {
		go func() {
			bg := context.Background()
			_ = webhookSvc.PublishEvent(bg, "tnt-default", ev)
		}()
	})
	// 起后台 dispatcher
	dispatchCtx, dispatchCancel := context.WithCancel(context.Background())
	t.Cleanup(dispatchCancel)
	go webhookSvc.Run(dispatchCtx)

	deps := &api.Deps{
		Store:    st,
		Broker:   broker,
		Agent:    agentSvc,
		Session:  sessionSvc,
		Memory:   memorySvc,
		Webhook:  webhookSvc,
		Harness:  hrns,
		AuthMode: "bypass",
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)

	return srv, mockProv
}

// HTTP helpers ----------------------------------------------------------------

func postJSON(t *testing.T, srv *httptest.Server, path string, body any, target any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if target != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("decode %s response: %v\nbody=%s", path, err, raw)
		}
	}
	return resp.StatusCode
}

func getJSON(t *testing.T, srv *httptest.Server, path string, target any) int {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if target != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("decode %s response: %v\nbody=%s", path, err, raw)
		}
	}
	return resp.StatusCode
}

// ─── Tests ────────────────────────────────────────────────────────────────

func TestHealth(t *testing.T) {
	srv, _ := setupHarness(t)
	var body map[string]any
	code := getJSON(t, srv, "/health", &body)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", body["status"])
	}
}

func TestCreateAgent(t *testing.T) {
	srv, _ := setupHarness(t)

	var agent map[string]any
	code := postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "test-agent",
		"model":  "gpt-5.5",
		"system": "You are a test assistant.",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &agent)
	if code != 201 {
		t.Fatalf("expected 201, got %d, body=%v", code, agent)
	}
	if id, _ := agent["id"].(string); !strings.HasPrefix(id, "agt-") {
		t.Fatalf("expected agt- id, got %v", agent["id"])
	}
	if agent["name"] != "test-agent" {
		t.Fatalf("expected name=test-agent, got %v", agent["name"])
	}
}

// TestE2E_HelloWorld 单轮无工具：user 发 hello，LLM 直接回文本。
func TestE2E_HelloWorld(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendText("hello back!")

	// 创建 agent
	var ag map[string]any
	code := postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "hello-agent",
		"model":  "mock-model",
		"system": "test",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	if code != 201 {
		t.Fatalf("create agent: %d", code)
	}
	agentID, _ := ag["id"].(string)

	// 创建 session
	var sess map[string]any
	code = postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent": agentID,
	}, &sess)
	if code != 201 {
		t.Fatalf("create session: %d, body=%v", code, sess)
	}
	sessionID, _ := sess["id"].(string)
	if !strings.HasPrefix(sessionID, "sess-") {
		t.Fatalf("expected sess- id, got %v", sessionID)
	}

	// 发 user message
	var acc map[string]any
	code = postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{
				"type":    "user.message",
				"content": []map[string]any{{"type": "text", "text": "hello"}},
			},
		},
	}, &acc)
	if code != 202 {
		t.Fatalf("post events: %d, body=%v", code, acc)
	}

	// 等到 idle (max 5s)
	events := waitForIdle(t, srv, sessionID, 5*time.Second)

	// 验证关键事件类型
	assertHasEventType(t, events, "user.message")
	assertHasEventType(t, events, "agent.message")
	assertHasEventType(t, events, "session.status_idle")

	// 验证 agent.message 含 "hello back!"
	msg := findFirstEvent(t, events, "agent.message")
	contentBlocks, _ := msg["content"].([]any)
	if len(contentBlocks) == 0 {
		t.Fatalf("agent.message empty content: %v", msg)
	}
	first, _ := contentBlocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "hello back!") {
		t.Fatalf("expected 'hello back!' in agent.message text, got %v", first["text"])
	}
}

// TestE2E_BashToolUse 双轮带工具：LLM 调 bash，看到 tool_result 后回复。
// 这是 MVP 的核心验收测试。
func TestE2E_BashToolUse(t *testing.T) {
	srv, mock := setupHarness(t)

	// 配 mock：第一轮 → bash echo；第二轮（收到 tool_result 后）→ 总结
	mock.AppendToolUse("bash", map[string]any{
		"command": "echo 'jadeenvoy is alive'",
	})
	mock.AppendFinalAfterTool("Confirmed: jadeenvoy is alive.")

	// 创建 agent
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "tool-agent",
		"model":  "mock-model",
		"system": "test with tools",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID, _ := ag["id"].(string)

	// 创建 session
	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID, _ := sess["id"].(string)

	// 发消息
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{
				"type":    "user.message",
				"content": []map[string]any{{"type": "text", "text": "are you alive?"}},
			},
		},
	}, nil)

	// 等 idle
	events := waitForIdle(t, srv, sessionID, 10*time.Second)

	// 验证事件序列：
	// 1. user.message
	// 2. session.status_running
	// 3. span.model_request_start (第 1 轮)
	// 4. span.model_request_end
	// 5. agent.tool_use (bash)
	// 6. agent.tool_result (含 "jadeenvoy is alive")
	// 7. span.model_request_start (第 2 轮)
	// 8. span.model_request_end
	// 9. agent.message (含 "Confirmed")
	// 10. session.status_idle

	assertHasEventType(t, events, "user.message")
	assertHasEventType(t, events, "session.status_running")
	assertHasEventType(t, events, "agent.tool_use")
	assertHasEventType(t, events, "agent.tool_result")
	assertHasEventType(t, events, "agent.message")
	assertHasEventType(t, events, "session.status_idle")

	// 验证 tool_use 调用了 bash
	tu := findFirstEvent(t, events, "agent.tool_use")
	if tu["name"] != "bash" {
		t.Fatalf("expected tool_use name=bash, got %v", tu["name"])
	}

	// 验证 tool_result 含命令输出
	tr := findFirstEvent(t, events, "agent.tool_result")
	content, _ := tr["content"].(string)
	if !strings.Contains(content, "jadeenvoy is alive") {
		t.Fatalf("expected tool_result content to contain 'jadeenvoy is alive', got %q", content)
	}
	if isErr, _ := tr["is_error"].(bool); isErr {
		t.Fatalf("tool_result should not be error: %v", tr)
	}

	// 验证最终 agent.message 在 tool_result 之后
	msgIdx := indexOfFirst(events, "agent.message")
	trIdx := indexOfFirst(events, "agent.tool_result")
	if msgIdx <= trIdx {
		t.Fatalf("expected agent.message after agent.tool_result (msgIdx=%d, trIdx=%d)", msgIdx, trIdx)
	}
	msg := findFirstEvent(t, events, "agent.message")
	contentBlocks, _ := msg["content"].([]any)
	first, _ := contentBlocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "Confirmed") {
		t.Fatalf("expected 'Confirmed' in final message, got %v", first["text"])
	}

	// 验证 session 转 idle
	var finalSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &finalSess)
	if finalSess["status"] != "idle" {
		t.Fatalf("expected session.status=idle, got %v", finalSess["status"])
	}

	// 验证 mock provider 被调了 2 次（一轮 LLM + 一轮 LLM）
	if got := mock.CalledCount(); got != 2 {
		t.Fatalf("expected mock called 2 times, got %d", got)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────

// waitForIdle 轮询 events 直到出现 session.status_idle 或超时。
func waitForIdle(t *testing.T, srv *httptest.Server, sessionID string, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var resp struct {
			Data []map[string]any `json:"data"`
		}
		getJSON(t, srv, "/v1/sessions/"+sessionID+"/events", &resp)
		for _, ev := range resp.Data {
			if ev["type"] == "session.status_idle" {
				return resp.Data
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for session.status_idle; events seen: %s", summarize(resp.Data))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func assertHasEventType(t *testing.T, events []map[string]any, want string) {
	t.Helper()
	for _, ev := range events {
		if ev["type"] == want {
			return
		}
	}
	t.Fatalf("missing event type %q in event log; got: %s", want, summarize(events))
}

func findFirstEvent(t *testing.T, events []map[string]any, want string) map[string]any {
	t.Helper()
	for _, ev := range events {
		if ev["type"] == want {
			return ev
		}
	}
	t.Fatalf("no event of type %q found", want)
	return nil
}

func indexOfFirst(events []map[string]any, want string) int {
	for i, ev := range events {
		if ev["type"] == want {
			return i
		}
	}
	return -1
}

func summarize(events []map[string]any) string {
	var sb strings.Builder
	for _, ev := range events {
		sb.WriteString(ev["type"].(string))
		sb.WriteString("\n")
	}
	return sb.String()
}
