package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestE2E_ToolGuardrailsDenyTool(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendToolUse("bash", map[string]any{"command": "echo should-not-run"})
	mock.AppendFinalAfterTool("guardrail handled")

	var ag map[string]any
	code := postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "guarded-agent",
		"model":  "mock-model",
		"system": "test guardrails",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
		"guardrails": map[string]any{
			"tool_permissions": map[string]any{
				"denied_tools": []string{"bash"},
			},
		},
		"metadata": map[string]string{"owner": "ops"},
	}, &ag)
	if code != 201 {
		t.Fatalf("create agent: %d body=%v", code, ag)
	}
	if _, leaked := ag["metadata"].(map[string]any)["_jadeenvoy_guardrails"]; leaked {
		t.Fatalf("guardrails metadata key leaked in response: %v", ag["metadata"])
	}
	guardrails, _ := ag["guardrails"].(map[string]any)
	if guardrails == nil {
		t.Fatalf("expected guardrails in agent response: %v", ag)
	}

	agentID, _ := ag["id"].(string)
	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID, _ := sess["id"].(string)
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{{
			"type":    "user.message",
			"content": []map[string]any{{"type": "text", "text": "try bash"}},
		}},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*time.Second)
	assertHasEventType(t, events, "agent.tool_use")
	assertHasEventType(t, events, "agent.guardrail_violation")
	assertHasEventType(t, events, "agent.tool_result")
	assertHasEventType(t, events, "agent.message")

	violation := findFirstEvent(t, events, "agent.guardrail_violation")
	reason, _ := violation["reason"].(string)
	if !strings.Contains(reason, "denied by agent guardrails") {
		t.Fatalf("unexpected guardrail reason: %v", violation)
	}
	result := findFirstEvent(t, events, "agent.tool_result")
	content, _ := result["content"].(string)
	if isErr, _ := result["is_error"].(bool); !isErr {
		t.Fatalf("expected denied tool_result to be an error: %v", result)
	}
	if strings.Contains(content, "should-not-run") {
		t.Fatalf("tool appears to have executed: %v", result)
	}
}

func TestE2E_ToolGuardrailsAllowedTools(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendToolUse("bash", map[string]any{"command": "echo not-allowed"})
	mock.AppendFinalAfterTool("allowlist handled")

	var ag map[string]any
	code := postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "allowlist-agent",
		"model":  "mock-model",
		"system": "test guardrails allowlist",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
		"guardrails": map[string]any{
			"tool_permissions": map[string]any{
				"allowed_tools": []string{"read", "write"},
			},
		},
	}, &ag)
	if code != 201 {
		t.Fatalf("create agent: %d body=%v", code, ag)
	}

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": ag["id"]}, &sess)
	sessionID, _ := sess["id"].(string)
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{{
			"type":    "user.message",
			"content": []map[string]any{{"type": "text", "text": "try bash"}},
		}},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*time.Second)
	violation := findFirstEvent(t, events, "agent.guardrail_violation")
	reason, _ := violation["reason"].(string)
	if !strings.Contains(reason, "allowed_tools") {
		t.Fatalf("unexpected allowlist reason: %v", violation)
	}
}
