package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeAntServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "wrong path", 404)
			return
		}
		if r.Header.Get("x-api-key") != "test-key" {
			http.Error(w, "auth", 401)
			return
		}
		if r.Header.Get("anthropic-version") == "" {
			http.Error(w, "missing version", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		for _, line := range lines {
			fmt.Fprint(w, line, "\n\n")
			flusher.Flush()
			time.Sleep(2 * time.Millisecond)
		}
	}))
}

func antEvent(typ string, payload map[string]any) string {
	payload["type"] = typ
	b, _ := json.Marshal(payload)
	return "event: " + typ + "\ndata: " + string(b)
}

func TestAnthropic_TextOnly(t *testing.T) {
	srv := fakeAntServer(t, []string{
		antEvent("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{"input_tokens": 10}}}),
		antEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text"}}),
		antEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "Hello"}}),
		antEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": " world"}}),
		antEvent("content_block_stop", map[string]any{"index": 0}),
		antEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 5}}),
		antEvent("message_stop", map[string]any{}),
	})
	defer srv.Close()

	p := NewAnthropic(srv.URL, "test-key", "anthropic")
	ch, err := p.Stream(context.Background(), ChatRequest{
		Model:    "claude-test",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var texts strings.Builder
	var stop *StopReason
	for ev := range ch {
		switch e := ev.(type) {
		case TextDelta:
			texts.WriteString(e.Text)
		case StopReason:
			s := e
			stop = &s
		}
	}
	if texts.String() != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", texts.String())
	}
	if stop == nil || stop.Reason != "end_turn" {
		t.Fatalf("expected end_turn stop, got %+v", stop)
	}
	if stop.Usage.InputTokens != 10 || stop.Usage.OutputTokens != 5 {
		t.Errorf("unexpected usage: %+v", stop.Usage)
	}
}

func TestAnthropic_ToolUse(t *testing.T) {
	srv := fakeAntServer(t, []string{
		antEvent("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{"input_tokens": 20}}}),
		antEvent("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "bash"}}),
		antEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"command":"`}}),
		antEvent("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `ls"}`}}),
		antEvent("content_block_stop", map[string]any{"index": 0}),
		antEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 8}}),
		antEvent("message_stop", map[string]any{}),
	})
	defer srv.Close()

	p := NewAnthropic(srv.URL, "test-key", "anthropic")
	ch, err := p.Stream(context.Background(), ChatRequest{
		Model:    "claude-test",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "list files"}}}},
		Tools:    []ToolDef{{Name: "bash", Description: "shell", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var toolStart *ToolUseStart
	var args strings.Builder
	var stop *StopReason
	for ev := range ch {
		switch e := ev.(type) {
		case ToolUseStart:
			s := e
			toolStart = &s
		case ToolUseDelta:
			args.WriteString(e.InputJSON)
		case StopReason:
			s := e
			stop = &s
		}
	}
	if toolStart == nil || toolStart.ID != "toolu_1" || toolStart.Name != "bash" {
		t.Fatalf("unexpected tool start: %+v", toolStart)
	}
	if args.String() != `{"command":"ls"}` {
		t.Errorf("accumulated args = %q", args.String())
	}
	if stop == nil || stop.Reason != "tool_use" {
		t.Errorf("expected tool_use stop, got %+v", stop)
	}
}

func TestAnthropic_RequestConversion(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "do it"}}},
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: "text", Text: "ok"},
			{Type: "tool_use", ToolUseID: "toolu_1", ToolName: "bash", ToolInput: json.RawMessage(`{"cmd":"ls"}`)},
		}},
		{Role: RoleUser, Content: []ContentBlock{
			{Type: "tool_result", ToolUseID: "toolu_1", ToolResult: "file.txt"},
		}},
	}
	req := buildAnthropicRequest(ChatRequest{System: "be helpful", Messages: msgs, MaxTokens: 100}, false)

	if req.System != "be helpful" || req.MaxTokens != 100 || !req.Stream {
		t.Errorf("unexpected top-level: %+v", req)
	}
	// assistant message: text + tool_use
	asst := req.Messages[1]
	if asst.Role != "assistant" || len(asst.Content) != 2 {
		t.Fatalf("bad assistant message: %+v", asst)
	}
	if asst.Content[1]["type"] != "tool_use" || asst.Content[1]["id"] != "toolu_1" || asst.Content[1]["name"] != "bash" {
		t.Errorf("bad tool_use block: %+v", asst.Content[1])
	}
	// tool_result goes under a user message
	ur := req.Messages[2]
	if ur.Role != "user" || ur.Content[0]["type"] != "tool_result" || ur.Content[0]["tool_use_id"] != "toolu_1" {
		t.Errorf("bad tool_result message: %+v", ur)
	}
}

// TestAnthropic_EmptyToolResultSerializesContent 回归：空 stdout 的 tool_result 仍须输出
// content 字段（否则 Anthropic /v1/messages 返回 400）。
func TestAnthropic_EmptyToolResultSerializesContent(t *testing.T) {
	req := buildAnthropicRequest(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: "tool_result", ToolUseID: "toolu_1", ToolResult: ""}}},
	}}, false)
	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), `"content":""`) {
		t.Errorf("empty tool_result must still emit content field, got: %s", b)
	}
}
