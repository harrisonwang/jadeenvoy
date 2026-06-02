package e2e

import (
	"strings"
	"testing"
	"time"
)

type dashboardCountJSON struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func TestE2E_AdminDashboardSnapshot(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendToolUse("bash", map[string]any{"command": "echo dashboard"})
	mock.AppendFinalAfterTool("dashboard observed")

	var ag map[string]any
	if code := postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "dashboard-agent",
		"model":  "mock-model",
		"system": "observe managed agent operations",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag); code != 201 {
		t.Fatalf("create agent: %d body=%v", code, ag)
	}
	agentID := ag["id"].(string)

	var ms map[string]any
	if code := postJSON(t, srv, "/v1/memory_stores", map[string]any{
		"name":        "dashboard-memory",
		"description": "ops context",
	}, &ms); code != 201 {
		t.Fatalf("create memory store: %d body=%v", code, ms)
	}
	storeID := ms["id"].(string)
	var mem map[string]any
	if code := postJSON(t, srv, "/v1/memory_stores/"+storeID+"/memories", map[string]any{
		"path":    "/runbooks/dashboard.md",
		"content": "watch session loops",
	}, &mem); code != 201 {
		t.Fatalf("create memory: %d body=%v", code, mem)
	}

	var v map[string]any
	if code := postJSON(t, srv, "/v1/vaults", map[string]any{"display_name": "Dashboard Vault"}, &v); code != 201 {
		t.Fatalf("create vault: %d body=%v", code, v)
	}
	vaultID := v["id"].(string)
	var cred map[string]any
	if code := postJSON(t, srv, "/v1/vaults/"+vaultID+"/credentials", map[string]any{
		"display_name": "GitHub",
		"auth": map[string]any{
			"type":           "static_bearer",
			"mcp_server_url": "https://github.example/mcp",
			"token":          "secret-token",
		},
	}, &cred); code != 201 {
		t.Fatalf("create credential: %d body=%v", code, cred)
	}

	var sess map[string]any
	if code := postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent": agentID,
		"resources": []map[string]any{{
			"type":            "memory_store",
			"memory_store_id": storeID,
			"access":          "read_only",
		}},
		"vault_ids": []string{vaultID},
	}, &sess); code != 201 {
		t.Fatalf("create session: %d body=%v", code, sess)
	}
	sessionID := sess["id"].(string)
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{{
			"type":    "user.message",
			"content": []map[string]any{{"type": "text", "text": "inspect dashboard"}},
		}},
	}, nil)
	waitForIdle(t, srv, sessionID, 10*time.Second)

	var body struct {
		Type    string `json:"type"`
		Runtime struct {
			Status            string `json:"status"`
			Database          string `json:"database"`
			AuthMode          string `json:"auth_mode"`
			LLMProvider       string `json:"llm_provider"`
			DefaultAgentModel string `json:"default_agent_model"`
		} `json:"runtime"`
		Counts struct {
			Agents           int64 `json:"agents"`
			Sessions         int64 `json:"sessions"`
			MemoryStores     int64 `json:"memory_stores"`
			Memories         int64 `json:"memories"`
			MemoryVersions   int64 `json:"memory_versions"`
			Vaults           int64 `json:"vaults"`
			VaultCredentials int64 `json:"vault_credentials"`
		} `json:"counts"`
		Sessions struct {
			Active   int64                `json:"active"`
			ByStatus []dashboardCountJSON `json:"by_status"`
		} `json:"sessions"`
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
		EventsByType []dashboardCountJSON `json:"events_by_type"`
		ToolActivity struct {
			BuiltInToolUses int64 `json:"built_in_tool_uses"`
			BuiltInResults  int64 `json:"built_in_results"`
			ErroredResults  int64 `json:"errored_results"`
		} `json:"tool_activity"`
		RecentSessions []struct {
			ID            string `json:"id"`
			AgentID       string `json:"agent_id"`
			Status        string `json:"status"`
			EventCount    int64  `json:"event_count"`
			LastEventType string `json:"last_event_type"`
		} `json:"recent_sessions"`
	}
	if code := getJSON(t, srv, "/admin/dashboard", &body); code != 200 {
		t.Fatalf("dashboard: %d body=%+v", code, body)
	}

	if body.Type != "dashboard_snapshot" || body.Runtime.Status != "ok" {
		t.Fatalf("bad dashboard identity/runtime: %+v", body)
	}
	if body.Runtime.Database != "sqlite" || body.Runtime.AuthMode != "bypass" || body.Runtime.LLMProvider != "mock" || body.Runtime.DefaultAgentModel != "mock-model" {
		t.Fatalf("unexpected runtime block: %+v", body.Runtime)
	}
	if body.Counts.Agents != 1 || body.Counts.Sessions != 1 || body.Sessions.Active != 1 {
		t.Fatalf("unexpected object counts: counts=%+v sessions=%+v", body.Counts, body.Sessions)
	}
	if body.Counts.MemoryStores != 1 || body.Counts.Memories != 1 || body.Counts.MemoryVersions != 1 {
		t.Fatalf("unexpected memory counts: %+v", body.Counts)
	}
	if body.Counts.Vaults != 1 || body.Counts.VaultCredentials != 1 {
		t.Fatalf("unexpected vault counts: %+v", body.Counts)
	}
	if body.Usage.TotalTokens == 0 {
		t.Fatalf("expected non-zero token usage: %+v", body.Usage)
	}
	if body.ToolActivity.BuiltInToolUses != 1 || body.ToolActivity.BuiltInResults != 1 || body.ToolActivity.ErroredResults != 0 {
		t.Fatalf("unexpected tool activity: %+v", body.ToolActivity)
	}
	if !dashboardHasCount(body.EventsByType, "agent.tool_use") || !dashboardHasCount(body.Sessions.ByStatus, "idle") {
		t.Fatalf("missing event/session breakdown: events=%+v sessions=%+v", body.EventsByType, body.Sessions.ByStatus)
	}
	if len(body.RecentSessions) != 1 {
		t.Fatalf("expected one recent session, got %+v", body.RecentSessions)
	}
	recent := body.RecentSessions[0]
	if recent.ID != sessionID || recent.AgentID != agentID || recent.Status != "idle" || recent.EventCount == 0 || strings.TrimSpace(recent.LastEventType) == "" {
		t.Fatalf("unexpected recent session: %+v", recent)
	}
}

func dashboardHasCount(items []dashboardCountJSON, name string) bool {
	for _, item := range items {
		if item.Name == name && item.Count > 0 {
			return true
		}
	}
	return false
}
