package e2e

import (
	"testing"
)

func TestE2E_AgentLifecycle(t *testing.T) {
	srv, _ := setupHarness(t)

	var ag map[string]any
	code := postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "lifecycle-agent",
		"model":  "mock-model-v1",
		"system": "v1",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	if code != 201 {
		t.Fatalf("create agent: %d", code)
	}
	agentID := ag["id"].(string)

	var updated map[string]any
	code = postJSON(t, srv, "/v1/agents/"+agentID, map[string]any{
		"name":    "lifecycle-agent-renamed",
		"model":   "mock-model-v2",
		"system":  "v2",
		"tools":   []map[string]any{{"type": "agent_toolset_20260401"}},
		"version": 1,
	}, &updated)
	if code != 200 {
		t.Fatalf("update agent: %d body=%v", code, updated)
	}
	if updated["version"].(float64) != 2 {
		t.Fatalf("expected version=2, got %v", updated["version"])
	}
	if updated["name"] != "lifecycle-agent-renamed" {
		t.Fatalf("expected renamed agent, got %v", updated["name"])
	}

	var conflict map[string]any
	code = postJSON(t, srv, "/v1/agents/"+agentID, map[string]any{
		"name":    "stale",
		"model":   "mock-model-v3",
		"tools":   []map[string]any{{"type": "agent_toolset_20260401"}},
		"version": 1,
	}, &conflict)
	if code != 409 {
		t.Fatalf("expected stale update 409, got %d body=%v", code, conflict)
	}

	var versions map[string]any
	code = getJSON(t, srv, "/v1/agents/"+agentID+"/versions", &versions)
	if code != 200 {
		t.Fatalf("versions: %d body=%v", code, versions)
	}
	data := versions["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(data))
	}

	var archived map[string]any
	code = postJSON(t, srv, "/v1/agents/"+agentID+"/archive", map[string]any{}, &archived)
	if code != 200 {
		t.Fatalf("archive agent: %d body=%v", code, archived)
	}
	if archived["archived_at"] == nil {
		t.Fatalf("expected archived_at, got %v", archived)
	}

	var del map[string]any
	code = postJSON(t, srv, "/v1/agents", map[string]any{
		"name":  "delete-me",
		"model": "mock-model",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &del)
	if code != 201 {
		t.Fatalf("create delete-me: %d", code)
	}
	deleteID := del["id"].(string)
	resp, err := srv.Client().Do(mustReq(t, "DELETE", srv.URL+"/v1/agents/"+deleteID, nil))
	if err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete agent status: %d", resp.StatusCode)
	}
}

func TestE2E_SessionUpdateArchive(t *testing.T) {
	srv, _ := setupHarness(t)

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":  "session-agent",
		"model": "mock-model",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": ag["id"]}, &sess)
	sessionID := sess["id"].(string)

	var updated map[string]any
	code := postJSON(t, srv, "/v1/sessions/"+sessionID, map[string]any{
		"title":     "renamed session",
		"metadata":  map[string]string{"k": "v"},
		"vault_ids": []string{"vlt-test"},
	}, &updated)
	if code != 200 {
		t.Fatalf("update session: %d body=%v", code, updated)
	}
	if updated["title"] != "renamed session" {
		t.Fatalf("expected renamed session, got %v", updated["title"])
	}

	var archived map[string]any
	code = postJSON(t, srv, "/v1/sessions/"+sessionID+"/archive", map[string]any{}, &archived)
	if code != 200 {
		t.Fatalf("archive session: %d body=%v", code, archived)
	}
	if archived["archived_at"] == nil {
		t.Fatalf("expected archived_at, got %v", archived)
	}
}
