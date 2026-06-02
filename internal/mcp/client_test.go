package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockMCPServer 是一个最小 Streamable HTTP MCP server，用于测试客户端。
// useSSE 控制 tools/call 用 SSE 还是 application/json 响应（client 都要支持）。
func mockMCPServer(t *testing.T, useSSE bool) *httptest.Server {
	t.Helper()
	var gotSessionID string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		// Accept 头必须同时声明两种类型。
		if acc := r.Header.Get("Accept"); !strings.Contains(acc, "application/json") || !strings.Contains(acc, "text/event-stream") {
			t.Errorf("Accept header missing required types: %q", acc)
		}
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		writeJSONRPC := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			out := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
			json.NewEncoder(w).Encode(out)
		}
		writeSSE := func(result any) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			out := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
			b, _ := json.Marshal(out)
			// 先来一条无关的 server notification，client 应跳过。
			w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
			w.Write([]byte("data: "))
			w.Write(b)
			w.Write([]byte("\n\n"))
		}

		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session-123")
			writeJSONRPC(map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "mock", "version": "1"},
			})
		case "notifications/initialized":
			// 通知校验 session id 已回带。
			if r.Header.Get("Mcp-Session-Id") != "test-session-123" {
				t.Errorf("initialized missing session id, got %q", r.Header.Get("Mcp-Session-Id"))
			}
			gotSessionID = r.Header.Get("Mcp-Session-Id")
			w.WriteHeader(202)
		case "tools/list":
			if gotSessionID == "" {
				t.Error("tools/list before initialized")
			}
			writeJSONRPC(map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "Echo back the input",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}},
					},
				},
			})
		case "tools/call":
			result := map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echoed: hi"}},
				"isError": false,
			}
			if useSSE {
				writeSSE(result)
			} else {
				writeJSONRPC(result)
			}
		default:
			w.WriteHeader(400)
		}
	}))
}

func runClientFlow(t *testing.T, useSSE bool) {
	t.Helper()
	srv := mockMCPServer(t, useSSE)
	defer srv.Close()
	ctx := context.Background()

	c := NewClient(srv.URL)
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if c.sessionID != "test-session-123" {
		t.Fatalf("expected session id captured, got %q", c.sessionID)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected isError")
	}
	if res.Text() != "echoed: hi" {
		t.Fatalf("unexpected result text: %q", res.Text())
	}
}

func TestMCPClient_JSONResponse(t *testing.T) { runClientFlow(t, false) }
func TestMCPClient_SSEResponse(t *testing.T)  { runClientFlow(t, true) }
