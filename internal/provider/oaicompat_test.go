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

// fakeOAIServer 返回一个 httptest server，按 lines 输出 SSE。
func fakeOAIServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "wrong path", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "auth", 401)
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
	return srv
}

// 把一个 OAI 流式 chunk 序列化成 SSE 行。
func chunk(content string, fr string) string {
	m := map[string]any{
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": fr}},
	}
	b, _ := json.Marshal(m)
	return "data: " + string(b)
}

func chunkToolCall(idx int, id, name, args string, fr string) string {
	delta := map[string]any{
		"tool_calls": []map[string]any{{
			"index":    idx,
			"id":       id,
			"function": map[string]any{"name": name, "arguments": args},
		}},
	}
	choice := map[string]any{"index": 0, "delta": delta}
	if fr != "" {
		choice["finish_reason"] = fr
	}
	m := map[string]any{"choices": []any{choice}}
	b, _ := json.Marshal(m)
	return "data: " + string(b)
}

func TestOAICompat_TextOnly(t *testing.T) {
	srv := fakeOAIServer(t, []string{
		chunk("Hello", ""),
		chunk(" world", ""),
		chunk("", "stop"),
		"data: [DONE]",
	})
	defer srv.Close()

	p := NewOAICompat(srv.URL, "test-key")
	ch, err := p.Stream(context.Background(), ChatRequest{
		Model:    "gpt-test",
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
	if stop == nil {
		t.Fatal("missing StopReason")
	}
	if stop.Reason != "end_turn" {
		t.Errorf("expected end_turn, got %q", stop.Reason)
	}
}

func TestOAICompat_ToolCalls(t *testing.T) {
	srv := fakeOAIServer(t, []string{
		chunkToolCall(0, "call_abc", "bash", "", ""),
		chunkToolCall(0, "", "", `{"command":"`, ""),
		chunkToolCall(0, "", "", `ls"}`, ""),
		`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
		"data: [DONE]",
	})
	defer srv.Close()

	p := NewOAICompat(srv.URL, "test-key")
	ch, err := p.Stream(context.Background(), ChatRequest{
		Model:    "gpt-test",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Tools: []ToolDef{{
			Name:        "bash",
			Description: "shell",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var toolStart *ToolUseStart
	var argsAccum strings.Builder
	var stop *StopReason
	for ev := range ch {
		switch e := ev.(type) {
		case ToolUseStart:
			s := e
			toolStart = &s
		case ToolUseDelta:
			argsAccum.WriteString(e.InputJSON)
		case StopReason:
			s := e
			stop = &s
		}
	}
	if toolStart == nil {
		t.Fatal("missing ToolUseStart")
	}
	if toolStart.ID != "call_abc" || toolStart.Name != "bash" {
		t.Errorf("unexpected tool start: %+v", toolStart)
	}
	wantArgs := `{"command":"ls"}`
	if argsAccum.String() != wantArgs {
		t.Errorf("accumulated args = %q, want %q", argsAccum.String(), wantArgs)
	}
	if stop == nil || stop.Reason != "tool_use" {
		t.Errorf("expected tool_use stop, got %+v", stop)
	}
}

func TestOAICompat_MessageConversion(t *testing.T) {
	// 测试 user → assistant tool_use → tool_result → assistant 的对话历史转换
	msgs := []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "do something"}}},
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: "text", Text: "Sure, let me run it"},
			{Type: "tool_use", ToolUseID: "call_1", ToolName: "bash", ToolInput: json.RawMessage(`{"cmd":"ls"}`)},
		}},
		{Role: RoleUser, Content: []ContentBlock{
			{Type: "tool_result", ToolUseID: "call_1", ToolResult: "file.txt"},
		}},
	}
	req := ChatRequest{
		System:   "you are helpful",
		Messages: msgs,
	}
	body := buildOAIRequest(req)
	if body.Messages[0].Role != "system" || body.Messages[0].Content != "you are helpful" {
		t.Errorf("expected system message first, got %+v", body.Messages[0])
	}
	// user message
	if body.Messages[1].Role != "user" {
		t.Errorf("expected user, got %+v", body.Messages[1])
	}
	// assistant with text + tool_calls
	if body.Messages[2].Role != "assistant" {
		t.Errorf("expected assistant, got %+v", body.Messages[2])
	}
	if len(body.Messages[2].ToolCalls) != 1 || body.Messages[2].ToolCalls[0].ID != "call_1" {
		t.Errorf("expected 1 tool_call w/ id call_1, got %+v", body.Messages[2].ToolCalls)
	}
	// tool result message
	if body.Messages[3].Role != "tool" || body.Messages[3].ToolCallID != "call_1" {
		t.Errorf("expected tool message w/ tool_call_id, got %+v", body.Messages[3])
	}
}
