package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// startMockMCPServer 起一个最小 Streamable HTTP MCP server，暴露一个 search 工具。
func startMockMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			w.Header().Set("Mcp-Session-Id", "sess-mcp-1")
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "kb", "version": "1"}})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{{
				"name":        "search",
				"description": "Search the internal knowledge base",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
			}}})
		case "tools/call":
			reply(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "KB result: deploy with helm"}},
				"isError": false,
			})
		default:
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestE2E_MCP_ToolCall 验证 agent 声明 MCP server → 工具被发现 → LLM 调用 → 路由到
// MCP server → agent.mcp_tool_use/result 事件 → 最终回复（ADR-0024）。
func TestE2E_MCP_ToolCall(t *testing.T) {
	mcpSrv := startMockMCPServer(t)
	srv, mock := setupHarness(t)

	// mock LLM：第 1 轮调 mcp__kb__search，收到结果后回复。
	mock.AppendToolUse("mcp__kb__search", map[string]any{"q": "how to deploy"})
	mock.AppendFinalAfterTool("Per the KB, deploy with helm.")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "mcp-agent",
		"model":  "mock-model",
		"system": "use mcp tools",
		"tools": []map[string]any{
			{"type": "agent_toolset_20260401"},
			{
				"type":            "mcp_toolset",
				"mcp_server_name": "kb",
				"default_config":  map[string]any{"permission_policy": map[string]any{"type": "always_allow"}},
			},
		},
		"mcp_servers": []map[string]any{
			{"type": "url", "name": "kb", "url": mcpSrv.URL},
		},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "how do I deploy?"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// MCP 专属事件类型（区别于内置 agent.tool_use）。
	assertHasEventType(t, events, "agent.mcp_tool_use")
	assertHasEventType(t, events, "agent.mcp_tool_result")

	tu := findFirstEvent(t, events, "agent.mcp_tool_use")
	if tu["name"] != "mcp__kb__search" {
		t.Fatalf("expected mcp__kb__search, got %v", tu["name"])
	}

	tr := findFirstEvent(t, events, "agent.mcp_tool_result")
	if c, _ := tr["content"].(string); !strings.Contains(c, "helm") {
		t.Fatalf("expected MCP result routed back, got %v", tr["content"])
	}

	// 最终回复整合了 MCP 结果。
	msg := findFirstEvent(t, events, "agent.message")
	blocks, _ := msg["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "helm") {
		t.Fatalf("expected final message to use MCP result, got %v", first["text"])
	}
}

// TestE2E_MCP_ServerDown_Degraded 验证 MCP server 连不上时 turn 不崩，正常降级回复（ADR-0024）。
func TestE2E_MCP_ServerDown_Degraded(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendText("answered without mcp")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "mcp-down-agent",
		"model":  "mock-model",
		"system": "x",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
		"mcp_servers": []map[string]any{
			{"type": "url", "name": "dead", "url": "http://127.0.0.1:1/mcp"}, // 必连失败
		},
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
	// 不该 terminated；应正常 idle 出回复。
	assertHasEventType(t, events, "agent.message")
	assertHasEventType(t, events, "session.status_idle")

	var checkSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &checkSess)
	if checkSess["status"] != "idle" {
		t.Fatalf("expected idle despite dead MCP server, got %v", checkSess["status"])
	}
}
