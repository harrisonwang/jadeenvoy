package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

func TestEstimateTokens(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.ContentBlock{{Type: "text", Text: "12345678"}}},              // 8 chars
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{{Type: "text", Text: "1234567890123456"}}}, // 16 chars
	}
	// 24 chars / 4 = 6
	if got := estimateTokens(msgs); got != 6 {
		t.Fatalf("expected 6, got %d", got)
	}
	if got := estimateTokens(nil); got != 0 {
		t.Fatalf("expected 0 for nil, got %d", got)
	}
}

func TestRenderTranscript(t *testing.T) {
	events := []*store.EventRow{
		{Seq: 1, Type: "user.message", Payload: json.RawMessage(`{"content":[{"type":"text","text":"hello"}]}`)},
		{Seq: 2, Type: "agent.message", Payload: json.RawMessage(`{"content":[{"type":"text","text":"hi back"}]}`)},
		{Seq: 3, Type: "agent.tool_use", Payload: json.RawMessage(`{"name":"bash"}`)},
		{Seq: 4, Type: "agent.tool_result", Payload: json.RawMessage(`{"content":"ok"}`)},
	}
	// 开区间 (0,5) → 全部
	out := renderTranscript(events, 0, 5)
	for _, want := range []string{"User: hello", "Assistant: hi back", "tool bash", "Tool result: ok"} {
		if !contains(out, want) {
			t.Fatalf("transcript missing %q in:\n%s", want, out)
		}
	}
	// after=2 → 只剩 seq 3,4
	out2 := renderTranscript(events, 2, 5)
	if contains(out2, "hello") {
		t.Fatalf("expected seq<=2 excluded, got:\n%s", out2)
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&provider.APIError{StatusCode: 503}, true},
		{&provider.APIError{StatusCode: 429}, true},
		{&provider.APIError{StatusCode: 400}, false},
		{&provider.APIError{StatusCode: 401}, false},
		{&provider.APIError{Type: "network"}, true},
		{&provider.APIError{Type: "overloaded_error"}, true},
		{&provider.APIError{Type: "weird"}, false},
		{errors.New("plain error"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("retryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestParseModelID(t *testing.T) {
	if got := parseModelID(json.RawMessage(`"claude-opus-4-8"`)); got != "claude-opus-4-8" {
		t.Fatalf("string form: got %q", got)
	}
	if got := parseModelID(json.RawMessage(`{"id":"m2","speed":"fast"}`)); got != "m2" {
		t.Fatalf("object form: got %q", got)
	}
}

func TestConnectMCP_NoServers(t *testing.T) {
	// 无 mcp_servers → nil；nil mcpSession 的方法应安全。
	var ms *mcpSession = connectMCP(context.Background(), json.RawMessage(`{}`), nil)
	if ms != nil {
		t.Fatalf("expected nil mcpSession for no servers")
	}
	if ms.isMCPTool("x") {
		t.Fatal("nil.isMCPTool should be false")
	}
	if ms.providerToolDefs() != nil {
		t.Fatal("nil.providerToolDefs should be nil")
	}
}

func TestParseCustomToolDefs(t *testing.T) {
	tools := json.RawMessage(`[
		{"type":"agent_toolset_20260401"},
		{"type":"custom","name":"my_tool","description":"d","input_schema":{"type":"object"}}
	]`)
	defs := parseCustomToolDefs(tools)
	if len(defs) != 1 || defs[0].Name != "my_tool" {
		t.Fatalf("expected 1 custom tool 'my_tool', got %+v", defs)
	}
}

func TestPermissionDecider(t *testing.T) {
	tools := json.RawMessage(`[
		{
			"type":"agent_toolset_20260401",
			"default_config":{"permission_policy":{"type":"always_ask"}},
			"configs":[{"name":"read","permission_policy":{"type":"always_allow"}}]
		},
		{
			"type":"mcp_toolset",
			"mcp_server_name":"github",
			"default_config":{"permission_policy":{"type":"always_allow"}},
			"configs":[{"name":"create_issue","permission_policy":{"type":"always_ask"}}]
		}
	]`)
	decider := newPermissionDecider(tools)
	if !decider.requiresApproval("bash", false, false) {
		t.Fatal("bash should inherit agent always_ask")
	}
	if decider.requiresApproval("read", false, false) {
		t.Fatal("read should use per-tool always_allow")
	}
	if decider.requiresApproval("mcp__github__list_repos", true, false) {
		t.Fatal("mcp list_repos should inherit server always_allow")
	}
	if !decider.requiresApproval("mcp__github__create_issue", true, false) {
		t.Fatal("mcp create_issue should use per-tool always_ask")
	}
	if decider.requiresApproval("my_custom", false, true) {
		t.Fatal("custom tools are controlled by user code, not permission_policy")
	}
}

func TestBuildMessagesMapsEventIDsToModelToolIDs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://"+t.TempDir()+"/messages.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sessionID := "sess-msg-map"
	now := int64(1)
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO session (id, tenant_id, agent_id, agent_version, agent_snapshot, status, created_at, updated_at) VALUES (?, 'tnt-default', 'agt-x', 1, '{}', 'idle', ?, ?)`, sessionID, now, now); err != nil {
		t.Fatal(err)
	}
	toolUse, err := st.AppendEvent(ctx, store.AppendEventInput{
		SessionID: sessionID,
		Type:      "agent.tool_use",
		Payload:   json.RawMessage(`{"type":"agent.tool_use","id":"toolu_model_1","name":"bash","input":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, store.AppendEventInput{
		SessionID: sessionID,
		Type:      "agent.tool_result",
		Payload:   json.RawMessage(`{"type":"agent.tool_result","tool_use_id":"` + toolUse.ID + `","content":"ok"}`),
	}); err != nil {
		t.Fatal(err)
	}
	h := &Harness{Store: st}
	msgs, err := h.buildMessages(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected assistant tool_use + user tool_result messages, got %+v", msgs)
	}
	if got := msgs[1].Content[0].ToolUseID; got != "toolu_model_1" {
		t.Fatalf("tool_result should map event id to model id, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
