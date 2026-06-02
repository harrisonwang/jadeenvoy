package e2e

import (
	"testing"
)

// TestE2E_DeleteSession_Succeeds 验证删除 session 的 HTTP 闭环（同时触发 sandbox 回收，
// 文件系统层面的回收由 internal/sandbox 单测覆盖）。
func TestE2E_DeleteSession_Succeeds(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendToolUse("bash", map[string]any{"command": "echo hi > /workspace/out.txt"})
	mock.AppendFinalAfterTool("done")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "cleanup-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "write a file"}}},
		},
	}, nil)
	waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 删除 session → 200，且 sandbox 回收在 handler 内同步触发（不报错即视为通过）。
	resp, err := srv.Client().Do(mustReq(t, "DELETE", srv.URL+"/v1/sessions/"+sessionID, nil))
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 deleting session, got %d", resp.StatusCode)
	}

	// 删除后 GET 应 404。
	var after map[string]any
	if code := getJSON(t, srv, "/v1/sessions/"+sessionID, &after); code != 404 {
		t.Fatalf("expected 404 after delete, got %d", code)
	}
}
