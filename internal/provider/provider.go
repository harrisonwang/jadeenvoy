// Package provider 是 LLM 调用抽象。
// 每个 provider 实现 Provider.Stream 返回 ChatEvent channel。
package provider

import (
	"context"
	"encoding/json"
)

// ─── 协议类型 ─────────────────────────────────────────────────────────────

// Role 是 message 角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是 chat history 中的一条。
type Message struct {
	Role    Role            `json:"role"`
	Content []ContentBlock  `json:"content"`
}

// ContentBlock 是 message 的内容块（text / tool_use / tool_result）。
type ContentBlock struct {
	Type        string          `json:"type"`         // text / tool_use / tool_result
	Text        string          `json:"text,omitempty"`
	ToolUseID   string          `json:"tool_use_id,omitempty"`   // for tool_result
	ToolName    string          `json:"name,omitempty"`           // for tool_use
	ToolInput   json.RawMessage `json:"input,omitempty"`          // for tool_use
	ToolResult  string          `json:"content,omitempty"`        // for tool_result (text)
	IsError     bool            `json:"is_error,omitempty"`       // for tool_result
}

// ToolDef 是给 LLM 看的工具定义。
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ChatRequest 是一轮 LLM 调用的请求。
type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDef
	MaxTokens   int
	Temperature float64
}

// ChatEvent 是流式输出的单元。
type ChatEvent interface {
	chatEvent()
}

type TextDelta struct{ Text string }

func (TextDelta) chatEvent() {}

type ToolUseStart struct {
	ID   string
	Name string
}

func (ToolUseStart) chatEvent() {}

type ToolUseDelta struct {
	ID        string
	InputJSON string // 累积的 input JSON 部分
}

func (ToolUseDelta) chatEvent() {}

type StopReason struct {
	Reason string // end_turn / tool_use / max_tokens / error
	Usage  Usage
}

func (StopReason) chatEvent() {}

type ErrorEvent struct {
	Type    string
	Message string
}

func (ErrorEvent) chatEvent() {}

type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// Provider 是 LLM 接口。
type Provider interface {
	Name() string
	Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
}
