package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestE2E_MultiAgentThreadRouting(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendText("research thread reply")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "multi-agent",
		"model":  "mock-model",
		"system": "coordinate threads",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
		"multiagent": map[string]any{
			"type": "multiagent.coordinator",
			"agents": []map[string]any{
				{"name": "researcher", "description": "research worker"},
			},
		},
	}, &ag)
	agentID := ag["id"].(string)
	if ma, _ := ag["multiagent"].(map[string]any); ma["type"] != "multiagent.coordinator" {
		t.Fatalf("multiagent config was not preserved: %v", ag["multiagent"])
	}

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	threadID := "worker_research"
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{
				"type":              "user.message",
				"session_thread_id": threadID,
				"content":           []map[string]any{{"type": "text", "text": "research this"}},
			},
		},
	}, nil)

	events := waitForEventType(t, srv, sessionID, "agent.message", int64(10*time.Second))
	msg := findFirstEvent(t, events, "agent.message")
	if msg["session_thread_id"] != threadID {
		t.Fatalf("agent.message should stay on %s, got %v", threadID, msg["session_thread_id"])
	}
	blocks, _ := msg["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "research thread") {
		t.Fatalf("unexpected thread reply: %v", first["text"])
	}

	var filtered struct {
		Data []map[string]any `json:"data"`
	}
	getJSON(t, srv, "/v1/sessions/"+sessionID+"/events?session_thread_id="+threadID, &filtered)
	if len(filtered.Data) == 0 {
		t.Fatal("expected filtered thread events")
	}
	for _, ev := range filtered.Data {
		if ev["session_thread_id"] != threadID {
			t.Fatalf("filtered events leaked another thread: %v", ev)
		}
	}
}
