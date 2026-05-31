package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicProvider 是 Anthropic Messages API 的 thin client。
//
// 同时服务 `anthropic`（官方 api.anthropic.com）和 `anthropic_compat`（自建 Anthropic
// 代理，base_url 可配）。
//
// 故意不依赖 anthropics/anthropic-sdk-go —— 遵循 ADR-0019 的零第三方依赖原则，自己写
// 一个 thin client 更可控、跨平台编译干净。内部 message 本就是 Anthropic 风格
// （见 ADR-0011），转换比 oaicompat 还简单。
type AnthropicProvider struct {
	BaseURL string            // e.g. "https://api.anthropic.com"
	APIKey  string            // x-api-key
	Version string            // anthropic-version header，默认 2023-06-01
	Headers map[string]string // 额外 header
	Client  *http.Client
	NameStr string // "anthropic" / "anthropic_compat"
}

func NewAnthropic(baseURL, apiKey, name string) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if name == "" {
		name = "anthropic"
	}
	return &AnthropicProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Version: "2023-06-01",
		Client:  &http.Client{Timeout: 600 * time.Second},
		NameStr: name,
	}
}

func (p *AnthropicProvider) Name() string {
	if p.NameStr == "" {
		return "anthropic"
	}
	return p.NameStr
}

func (p *AnthropicProvider) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	body := buildAnthropicRequest(req)
	bodyBytes, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.BaseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", p.Version)
	if p.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.APIKey)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(raw))
	}

	ch := make(chan ChatEvent, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		parseAnthropicStream(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// ─── 请求构造 ─────────────────────────────────────────────────────────────

type antRequest struct {
	Model       string       `json:"model"`
	MaxTokens   int          `json:"max_tokens"`
	System      string       `json:"system,omitempty"`
	Messages    []antMessage `json:"messages"`
	Tools       []antTool    `json:"tools,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	Stream      bool         `json:"stream"`
}

type antMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func buildAnthropicRequest(req ChatRequest) antRequest {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	out := antRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    req.System,
		Stream:    true,
	}
	if req.Temperature > 0 {
		t := req.Temperature
		out.Temperature = &t
	}
	for _, m := range req.Messages {
		role := string(m.Role)
		if role != "assistant" {
			role = "user" // Anthropic 仅 user/assistant；tool_result 归 user
		}
		am := antMessage{Role: role}
		for _, b := range m.Content {
			am.Content = append(am.Content, toAntBlock(b))
		}
		out.Messages = append(out.Messages, am)
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, antTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

func toAntBlock(b ContentBlock) map[string]any {
	switch b.Type {
	case "tool_use":
		in := b.ToolInput
		if len(in) == 0 {
			in = json.RawMessage(`{}`)
		}
		return map[string]any{"type": "tool_use", "id": b.ToolUseID, "name": b.ToolName, "input": in}
	case "tool_result":
		// content 必须显式输出：空 stdout 的 tool_result 若省略 content 字段，Anthropic 返回 400。
		return map[string]any{"type": "tool_result", "tool_use_id": b.ToolUseID, "content": b.ToolResult, "is_error": b.IsError}
	default: // text
		return map[string]any{"type": "text", "text": b.Text}
	}
}

// ─── 流解析 ──────────────────────────────────────────────────────────────

type antStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage antUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *antUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type antUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func parseAnthropicStream(ctx context.Context, body io.Reader, ch chan<- ChatEvent) {
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 4*1024*1024)

	toolIndexToID := map[int]string{}
	var usage Usage
	stop := "end_turn"

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // 忽略 "event:" 行；type 在 data JSON 里
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var ev antStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				usage.InputTokens = ev.Message.Usage.InputTokens
				usage.OutputTokens = ev.Message.Usage.OutputTokens // 初值；message_delta 会给最终值
				usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
				usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				toolIndexToID[ev.Index] = ev.ContentBlock.ID
				ch <- ToolUseStart{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- TextDelta{Text: ev.Delta.Text}
				}
			case "input_json_delta":
				ch <- ToolUseDelta{ID: toolIndexToID[ev.Index], InputJSON: ev.Delta.PartialJSON}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stop = mapAnthropicStop(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "error":
			etype, emsg := "anthropic_error", data
			if ev.Error != nil {
				if ev.Error.Type != "" {
					etype = ev.Error.Type
				}
				if ev.Error.Message != "" {
					emsg = ev.Error.Message
				}
			}
			ch <- ErrorEvent{Type: etype, Message: emsg}
			ch <- StopReason{Reason: "error", Usage: usage}
			return
		case "message_stop":
			ch <- StopReason{Reason: stop, Usage: usage}
			return
		}
	}
	// 流提前结束也补一个 StopReason，防 harness 卡住
	ch <- StopReason{Reason: stop, Usage: usage}
}

func mapAnthropicStop(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "end_turn"
	case "tool_use":
		return "tool_use"
	case "max_tokens":
		return "max_tokens"
	default:
		return reason
	}
}
