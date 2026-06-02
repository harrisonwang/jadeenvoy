package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestE2E_AlwaysAskToolConfirmation(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendToolUse("bash", map[string]any{"command": "printf approved"})
	mock.AppendFinalAfterTool("tool was approved")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "ask-agent",
		"model":  "mock-model",
		"system": "confirm tools",
		"tools": []map[string]any{
			{
				"type":           "agent_toolset_20260401",
				"default_config": map[string]any{"permission_policy": map[string]any{"type": "always_ask"}},
			},
		},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "run it"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*time.Second)
	tu := findFirstEvent(t, events, "agent.tool_use")
	if tu["name"] != "bash" {
		t.Fatalf("expected bash tool_use, got %v", tu["name"])
	}
	if tu["requires_confirmation"] != true {
		t.Fatalf("tool_use should require confirmation: %v", tu)
	}
	for _, ev := range events {
		if ev["type"] == "agent.tool_result" {
			t.Fatalf("tool_result must not be emitted before confirmation: %v", ev)
		}
	}
	idle := findFirstEvent(t, events, "session.status_idle")
	stop, _ := idle["stop_reason"].(map[string]any)
	if stop["type"] != "requires_action" {
		t.Fatalf("expected requires_action stop reason, got %v", stop)
	}

	code := postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{
				"type":        "user.tool_confirmation",
				"tool_use_id": tu["id"],
				"result":      "allow",
			},
		},
	}, nil)
	if code != 202 {
		t.Fatalf("post tool_confirmation: %d", code)
	}

	events = waitForEventType(t, srv, sessionID, "agent.message", int64(10*time.Second))
	tr := findFirstEvent(t, events, "agent.tool_result")
	if c, _ := tr["content"].(string); !strings.Contains(c, "approved") {
		t.Fatalf("expected approved command output, got %v", tr["content"])
	}
	msg := findFirstEvent(t, events, "agent.message")
	blocks, _ := msg["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "approved") {
		t.Fatalf("expected final message after tool result, got %v", first["text"])
	}
	if got := mock.CalledCount(); got != 2 {
		t.Fatalf("expected mock called twice, got %d", got)
	}
}
