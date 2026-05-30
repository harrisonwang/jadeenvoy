package harness

import (
	"context"
	"encoding/json"

	"github.com/harrisonwang/jadeenvoy/internal/provider"
)

// buildMessages 从 event log 拼出给 LLM 的 messages。
func (h *Harness) buildMessages(ctx context.Context, sessionID string) ([]provider.Message, error) {
	events, err := h.Store.ListEvents(ctx, sessionID, nil)
	if err != nil {
		return nil, err
	}

	var msgs []provider.Message
	var current *provider.Message
	flush := func() {
		if current != nil && len(current.Content) > 0 {
			msgs = append(msgs, *current)
		}
		current = nil
	}
	ensure := func(role provider.Role) {
		if current == nil || current.Role != role {
			flush()
			current = &provider.Message{Role: role}
		}
	}

	for _, ev := range events {
		switch ev.Type {
		case "user.message":
			ensure(provider.RoleUser)
			var payload struct {
				Content []provider.ContentBlock `json:"content"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			for _, b := range payload.Content {
				if b.Type == "" {
					b.Type = "text"
				}
				current.Content = append(current.Content, b)
			}
		case "agent.message":
			ensure(provider.RoleAssistant)
			var payload struct {
				Content []provider.ContentBlock `json:"content"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			for _, b := range payload.Content {
				current.Content = append(current.Content, b)
			}
		case "agent.tool_use", "agent.custom_tool_use":
			ensure(provider.RoleAssistant)
			var payload struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			current.Content = append(current.Content, provider.ContentBlock{
				Type:      "tool_use",
				ToolUseID: payload.ID,
				ToolName:  payload.Name,
				ToolInput: payload.Input,
			})
		case "agent.tool_result", "user.custom_tool_result":
			ensure(provider.RoleUser) // tool_result 跟 user message 同 role（Anthropic 协议）
			var payload struct {
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
				IsError   bool   `json:"is_error"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			current.Content = append(current.Content, provider.ContentBlock{
				Type:       "tool_result",
				ToolUseID:  payload.ToolUseID,
				ToolResult: payload.Content,
				IsError:    payload.IsError,
			})
		}
	}
	flush()
	return msgs, nil
}
