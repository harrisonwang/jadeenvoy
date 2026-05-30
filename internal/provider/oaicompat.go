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

// OAICompatProvider 是 OpenAI 兼容 LLM 网关的 thin client。
//
// 兼容: OpenAI / Azure OpenAI / DeepSeek / vLLM / new-api / one-api / litellm
// 等任何实现了 POST /chat/completions 的网关。
//
// 故意不依赖 go-openai —— 依赖太重且 API 变化频繁，自己写 200 行更可控。
// 详见 .docs/30-adr/0011-llm-provider-abstraction.md。
type OAICompatProvider struct {
	BaseURL string            // e.g. "https://api.openai.com/v1" or 公司内网网关
	APIKey  string            // Bearer token
	Headers map[string]string // 额外 header（如 X-Tenant）
	Client  *http.Client
	NameStr string // provider 名称（report 用，默认 "openai_compat"）
}

func NewOAICompat(baseURL, apiKey string) *OAICompatProvider {
	return &OAICompatProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 600 * time.Second},
		NameStr: "openai_compat",
	}
}

func (p *OAICompatProvider) Name() string {
	if p.NameStr == "" {
		return "openai_compat"
	}
	return p.NameStr
}

func (p *OAICompatProvider) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	body := buildOAIRequest(req)
	bodyBytes, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.BaseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
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
		parseOAIStream(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// ─── 请求构造 ─────────────────────────────────────────────────────────────

type oaiMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"` // string 或 []part
	ToolCalls  []oaiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type oaiToolCall struct {
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`     // "function"
	Function oaiCallFunction `json:"function"`
}

type oaiCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type oaiToolDef struct {
	Type     string          `json:"type"`
	Function oaiFunctionDef  `json:"function"`
}

type oaiFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiRequest struct {
	Model     string         `json:"model"`
	Messages  []oaiMessage   `json:"messages"`
	Tools     []oaiToolDef   `json:"tools,omitempty"`
	MaxTokens int            `json:"max_tokens,omitempty"`
	Stream    bool           `json:"stream"`
	StreamOpts map[string]bool `json:"stream_options,omitempty"`
}

func buildOAIRequest(req ChatRequest) oaiRequest {
	out := oaiRequest{
		Model:      req.Model,
		MaxTokens:  req.MaxTokens,
		Stream:     true,
		StreamOpts: map[string]bool{"include_usage": true},
	}
	if req.System != "" {
		out.Messages = append(out.Messages, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, convertMessageToOAI(m)...)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, oaiToolDef{
			Type: "function",
			Function: oaiFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}

// convertMessageToOAI 把 provider.Message（Anthropic 风格）转 OpenAI 风格。
//
// 一条 Anthropic assistant message 可能含 text + tool_use；OpenAI 这是一条消息含
// content + tool_calls。
// 一条 Anthropic user message 含 tool_result；OpenAI 这是单独的 "tool" 角色消息。
func convertMessageToOAI(m Message) []oaiMessage {
	role := string(m.Role)
	if role == "" {
		role = "user"
	}

	// 拆分: text/tool_use 累积到 assistant；tool_result 各自一条 tool 消息
	var assistant *oaiMessage
	var toolResults []oaiMessage
	var userText strings.Builder

	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if role == "assistant" {
				if assistant == nil {
					assistant = &oaiMessage{Role: "assistant"}
				}
				if txt, _ := assistant.Content.(string); txt != "" {
					assistant.Content = txt + b.Text
				} else {
					assistant.Content = b.Text
				}
			} else {
				userText.WriteString(b.Text)
			}
		case "tool_use":
			if assistant == nil {
				assistant = &oaiMessage{Role: "assistant"}
			}
			args := string(b.ToolInput)
			if args == "" {
				args = "{}"
			}
			assistant.ToolCalls = append(assistant.ToolCalls, oaiToolCall{
				ID:   b.ToolUseID,
				Type: "function",
				Function: oaiCallFunction{
					Name:      b.ToolName,
					Arguments: args,
				},
			})
		case "tool_result":
			toolResults = append(toolResults, oaiMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    b.ToolResult,
			})
		}
	}

	var out []oaiMessage
	if assistant != nil {
		out = append(out, *assistant)
	}
	if userText.Len() > 0 {
		out = append(out, oaiMessage{Role: role, Content: userText.String()})
	}
	out = append(out, toolResults...)
	return out
}

// ─── 流解析 ──────────────────────────────────────────────────────────────

type oaiStreamResp struct {
	Choices []oaiChoice `json:"choices"`
	Usage   *oaiUsage   `json:"usage"`
}

type oaiChoice struct {
	Index        int           `json:"index"`
	Delta        oaiDelta      `json:"delta"`
	FinishReason string        `json:"finish_reason"`
}

type oaiDelta struct {
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	ToolCalls []oaiToolCallDelta `json:"tool_calls"`
}

type oaiToolCallDelta struct {
	Index    int             `json:"index"`
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiCallFunction `json:"function"`
}

type oaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

func parseOAIStream(ctx context.Context, body io.Reader, ch chan<- ChatEvent) {
	scanner := bufio.NewScanner(body)
	// SSE 行可能很长，调大 buffer
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 4*1024*1024)

	// 跟踪 tool call 累积（key = index）
	toolByIndex := map[int]*toolCallAccum{}
	var usage Usage
	stopReason := "end_turn"
	stopReasonSet := false

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}

		var resp oaiStreamResp
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue // 跳过解析失败的行（兼容某些网关额外 keepalive 行）
		}

		if resp.Usage != nil {
			usage.InputTokens = resp.Usage.PromptTokens
			usage.OutputTokens = resp.Usage.CompletionTokens
		}

		for _, ch0 := range resp.Choices {
			// content delta
			if ch0.Delta.Content != "" {
				ch <- TextDelta{Text: ch0.Delta.Content}
			}
			// tool calls
			for _, tc := range ch0.Delta.ToolCalls {
				acc, ok := toolByIndex[tc.Index]
				if !ok {
					acc = &toolCallAccum{}
					toolByIndex[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
					ch <- ToolUseStart{ID: acc.ID, Name: acc.Name}
				}
				if tc.Function.Arguments != "" {
					acc.Arguments += tc.Function.Arguments
					ch <- ToolUseDelta{ID: acc.ID, InputJSON: tc.Function.Arguments}
				}
			}
			// finish_reason
			if ch0.FinishReason != "" {
				switch ch0.FinishReason {
				case "stop":
					stopReason = "end_turn"
				case "tool_calls":
					stopReason = "tool_use"
				case "length":
					stopReason = "max_tokens"
				default:
					stopReason = ch0.FinishReason
				}
				stopReasonSet = true
			}
		}
	}

	if !stopReasonSet {
		// 网关没明确给 finish_reason；假设 end_turn
		stopReason = "end_turn"
	}
	ch <- StopReason{Reason: stopReason, Usage: usage}

	if err := scanner.Err(); err != nil {
		// scanner 错误已是流末尾，stopReason 已发，吞掉即可
		_ = err
	}
}

type toolCallAccum struct {
	ID        string
	Name      string
	Arguments string
}
