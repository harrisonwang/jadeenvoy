package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// startAuthedMockMCP 起一个要求 Authorization: Bearer <wantToken> 的 MCP server。
// 无/错 token → 401。
func startAuthedMockMCP(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(401)
			return
		}
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "sec", "version": "1"}})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{{
				"name": "secret", "description": "secured tool",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			reply(map[string]any{"content": []map[string]any{{"type": "text", "text": "authed ok"}}, "isError": false})
		default:
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestE2E_MCP_VaultAuth 验证：session 带 vault_ids，vault 里有匹配 host 的 static_bearer
// 凭据 → MCP 请求自动注入 Authorization → 鉴权 server 调用成功（ADR-0026）。
func TestE2E_MCP_VaultAuth(t *testing.T) {
	const token = "secret-mcp-token-xyz"
	mcpSrv := startAuthedMockMCP(t, token)
	srv, mock := setupHarness(t)

	mock.AppendToolUse("mcp__sec__secret", map[string]any{})
	mock.AppendFinalAfterTool("done via authed mcp")

	// 1. 建 vault + 该 host 的 static_bearer 凭据。
	var v map[string]any
	postJSON(t, srv, "/v1/vaults", map[string]any{"display_name": "mcp-creds"}, &v)
	vid := v["id"].(string)
	code := postJSON(t, srv, "/v1/vaults/"+vid+"/credentials", map[string]any{
		"display_name": "sec-server",
		"auth":         map[string]any{"type": "static_bearer", "mcp_server_url": mcpSrv.URL, "token": token},
	}, nil)
	if code != 201 {
		t.Fatalf("add credential: %d", code)
	}

	// 2. agent 声明该 MCP server。
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "mcp-auth-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{
			{"type": "agent_toolset_20260401"},
			{
				"type":            "mcp_toolset",
				"mcp_server_name": "sec",
				"default_config":  map[string]any{"permission_policy": map[string]any{"type": "always_allow"}},
			},
		},
		"mcp_servers": []map[string]any{{"type": "url", "name": "sec", "url": mcpSrv.URL}},
	}, &ag)
	agentID := ag["id"].(string)

	// 3. session 绑定 vault。
	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent":     agentID,
		"vault_ids": []string{vid},
	}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "use secured tool"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// MCP 调用应成功（token 注入生效），结果回喂。
	assertHasEventType(t, events, "agent.mcp_tool_use")
	tr := findFirstEvent(t, events, "agent.mcp_tool_result")
	if ie, _ := tr["is_error"].(bool); ie {
		t.Fatalf("expected authed MCP call to succeed, got error result: %v", tr["content"])
	}
	if c, _ := tr["content"].(string); !strings.Contains(c, "authed ok") {
		t.Fatalf("expected 'authed ok' from server, got %v", tr["content"])
	}
}

// TestE2E_MCP_NoVault_Unauthorized 验证：不绑 vault → 无 token → 鉴权 server 拒绝，
// MCP 调用返回 is_error（但 turn 不崩，degraded）。
func TestE2E_MCP_NoVault_Unauthorized(t *testing.T) {
	mcpSrv := startAuthedMockMCP(t, "the-token")
	srv, mock := setupHarness(t)
	mock.AppendText("no mcp tools available")

	// agent 声明 server，但 server 在 initialize 阶段就要 401 → 工具发现失败 → 无 MCP 工具。
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "mcp-noauth-agent", "model": "mock-model", "system": "x",
		"tools":       []map[string]any{{"type": "agent_toolset_20260401"}},
		"mcp_servers": []map[string]any{{"type": "url", "name": "sec", "url": mcpSrv.URL}},
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

	// 工具发现失败应被 degraded 处理：turn 正常完成，不 terminated。
	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)
	for _, ev := range events {
		if ev["type"] == "session.status_terminated" {
			t.Fatal("unauthorized MCP discovery must degrade, not terminate")
		}
	}
	assertHasEventType(t, events, "agent.message")
}
