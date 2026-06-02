package harness

import (
	"context"
	"encoding/json"

	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// buildMessages 从 event log 拼出给 LLM 的 messages。
//
// compaction-aware（ADR-0021）：若存在 agent.thread_context_compacted checkpoint，
// 跳过 seq <= through_seq 的事件，并把 summary 作为前缀注入到保留段第一条 user message，
// 既缩短上下文又不破坏 user/assistant role 交替。
func (h *Harness) buildMessages(ctx context.Context, sessionID string, threadIDs ...string) ([]provider.Message, error) {
	threadID := "primary"
	if len(threadIDs) > 0 && threadIDs[0] != "" {
		threadID = threadIDs[0]
	}
	events, err := h.Store.ListEvents(ctx, sessionID, nil)
	if err != nil {
		return nil, err
	}
	events = filterThreadEvents(events, threadID)

	// 找最新 checkpoint 的 through_seq + summary。
	cutoff := int64(-1)
	summaryPrefix := ""
	maxCPSeq := int64(-1)
	for _, ev := range events {
		if ev.Type != compactionEventType || ev.Seq <= maxCPSeq {
			continue
		}
		var p compactionPayload
		if json.Unmarshal(ev.Payload, &p) == nil {
			maxCPSeq = ev.Seq
			cutoff = p.ThroughSeq
			summaryPrefix = p.Summary
		}
	}
	summaryPending := summaryPrefix != ""
	toolUseEventToModelID := map[string]string{}
	for _, ev := range events {
		switch ev.Type {
		case "agent.tool_use", "agent.custom_tool_use", "agent.mcp_tool_use":
			var payload struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(ev.Payload, &payload) == nil && payload.ID != "" {
				toolUseEventToModelID[ev.ID] = payload.ID
			}
		}
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
		if ev.Seq <= cutoff {
			continue // 已被 checkpoint 摘要取代
		}
		switch ev.Type {
		case "user.message":
			ensure(provider.RoleUser)
			var payload struct {
				Content []provider.ContentBlock `json:"content"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			// 把摘要注入保留段第一条 user message 之前。
			if summaryPending {
				current.Content = append(current.Content, provider.ContentBlock{
					Type: "text",
					Text: "[Earlier conversation summary]\n" + summaryPrefix,
				})
				summaryPending = false
			}
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
		case "agent.tool_use", "agent.custom_tool_use", "agent.mcp_tool_use":
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
		case "agent.tool_result", "user.custom_tool_result", "agent.mcp_tool_result":
			ensure(provider.RoleUser) // tool_result 跟 user message 同 role（Anthropic 协议）
			var payload struct {
				ToolUseID       string `json:"tool_use_id"`
				CustomToolUseID string `json:"custom_tool_use_id"`
				Content         string `json:"content"`
				IsError         bool   `json:"is_error"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			if payload.ToolUseID == "" {
				payload.ToolUseID = payload.CustomToolUseID
			}
			if modelID := toolUseEventToModelID[payload.ToolUseID]; modelID != "" {
				payload.ToolUseID = modelID
			}
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

func filterThreadEvents(events []*store.EventRow, threadID string) []*store.EventRow {
	if threadID == "" {
		threadID = "primary"
	}
	out := events[:0]
	for _, ev := range events {
		if ev.ThreadID == "" || ev.ThreadID == threadID {
			out = append(out, ev)
		}
	}
	return out
}
