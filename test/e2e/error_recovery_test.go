package e2e

import (
	"testing"

	"github.com/harrisonwang/jadeenvoy/internal/harness"
	"github.com/harrisonwang/jadeenvoy/internal/provider"
)

// TestE2E_PermanentError_Terminates 验证不可重试错误（4xx）→ session.status_terminated，
// 不再卡在 running（ADR-0022）。
func TestE2E_PermanentError_Terminates(t *testing.T) {
	srv, mock := setupHarnessWith(t, func(h *harness.Harness) {
		h.MaxRetries = 2
	})
	// 永久错误：400。Match 一律命中。
	mock.AppendError(&provider.APIError{StatusCode: 400, Type: "invalid_request_error", Message: "bad"})

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "err-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "hi"}}},
		},
	}, nil)

	events := waitForEventType(t, srv, sessionID, "session.status_terminated", 10*1000_000_000)
	assertHasEventType(t, events, "session.status_running")

	// DB 状态应为 terminated，绝不能停在 running。
	var checkSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &checkSess)
	if checkSess["status"] != "terminated" {
		t.Fatalf("expected status=terminated, got %v", checkSess["status"])
	}

	// 不该重试永久错误：mock 只被调 1 次。
	if got := mock.CalledCount(); got != 1 {
		t.Fatalf("permanent error must not retry; expected 1 call, got %d", got)
	}
}

// TestE2E_TransientError_RetriesThenRecovers 验证瞬时错误（503）→ 先 rescheduled
// 重试 → 成功后正常 idle（ADR-0022）。
func TestE2E_TransientError_RetriesThenRecovers(t *testing.T) {
	srv, mock := setupHarnessWith(t, func(h *harness.Harness) {
		h.MaxRetries = 3
	})
	// 第 1 次调用：503 瞬时错误；之后：正常回复。AppendErrorOnce 仅命中一次。
	mock.AppendErrorOnce(&provider.APIError{StatusCode: 503, Type: "overloaded_error", Message: "busy"})
	mock.AppendText("recovered hello")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "retry-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "hi"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 重试期间应发过 rescheduled，最终自然结束。
	assertHasEventType(t, events, "session.status_rescheduled")
	assertHasEventType(t, events, "agent.message")
	idle := findFirstEvent(t, events, "session.status_idle")
	if sr, _ := idle["stop_reason"].(map[string]any); sr["type"] != "end_turn" {
		t.Fatalf("expected end_turn after recovery, got %v", idle["stop_reason"])
	}

	var checkSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &checkSess)
	if checkSess["status"] != "idle" {
		t.Fatalf("expected status=idle after recovery, got %v", checkSess["status"])
	}
}
