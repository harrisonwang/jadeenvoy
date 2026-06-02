package harness

import (
	"context"
	"encoding/json"

	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

type pendingConfirmation struct {
	EventID        string
	EventType      string
	ModelToolUseID string
	Name           string
	Input          json.RawMessage
}

type toolConfirmation struct {
	Result      string
	DenyMessage string
}

func (h *Harness) resolvePendingToolConfirmations(ctx context.Context, sessionID, threadID string, sb sandbox.Sandbox, mcpSess *mcpSession) error {
	events, err := h.Store.ListEvents(ctx, sessionID, nil)
	if err != nil {
		return err
	}
	events = filterThreadEvents(events, threadID)
	pending := collectPendingConfirmations(events)
	confirmations := collectToolConfirmations(events)
	resolved := collectResolvedToolUseEvents(events)
	for _, p := range pending {
		if resolved[p.EventID] {
			continue
		}
		c, ok := confirmations[p.EventID]
		if !ok {
			continue
		}
		resultType := toolResultEventType(p.EventType)
		if c.Result == "deny" {
			msg := c.DenyMessage
			if msg == "" {
				msg = "tool use denied by user"
			}
			h.publishToolResult(ctx, sessionID, threadID, resultType, p.ModelToolUseID, p.EventID, msg, true)
			continue
		}
		if c.Result != "allow" {
			continue
		}
		content, isErr := h.executeServerTool(ctx, sb, mcpSess, p.Name, p.Input)
		h.publishToolResult(ctx, sessionID, threadID, resultType, p.ModelToolUseID, p.EventID, content, isErr)
	}
	return nil
}

func collectPendingConfirmations(events []*store.EventRow) []pendingConfirmation {
	var out []pendingConfirmation
	for _, ev := range events {
		if ev.Type != "agent.tool_use" && ev.Type != "agent.mcp_tool_use" {
			continue
		}
		var payload struct {
			ID                   string          `json:"id"`
			Name                 string          `json:"name"`
			Input                json.RawMessage `json:"input"`
			RequiresConfirmation bool            `json:"requires_confirmation"`
		}
		if json.Unmarshal(ev.Payload, &payload) != nil || !payload.RequiresConfirmation {
			continue
		}
		if len(payload.Input) == 0 {
			payload.Input = json.RawMessage(`{}`)
		}
		out = append(out, pendingConfirmation{
			EventID:        ev.ID,
			EventType:      ev.Type,
			ModelToolUseID: payload.ID,
			Name:           payload.Name,
			Input:          payload.Input,
		})
	}
	return out
}

func collectToolConfirmations(events []*store.EventRow) map[string]toolConfirmation {
	out := map[string]toolConfirmation{}
	for _, ev := range events {
		if ev.Type != "user.tool_confirmation" {
			continue
		}
		var payload struct {
			ToolUseID   string `json:"tool_use_id"`
			Result      string `json:"result"`
			DenyMessage string `json:"deny_message"`
		}
		if json.Unmarshal(ev.Payload, &payload) != nil || payload.ToolUseID == "" {
			continue
		}
		out[payload.ToolUseID] = toolConfirmation{Result: payload.Result, DenyMessage: payload.DenyMessage}
	}
	return out
}

func collectResolvedToolUseEvents(events []*store.EventRow) map[string]bool {
	modelToEvent := map[string]string{}
	resolved := map[string]bool{}
	for _, ev := range events {
		switch ev.Type {
		case "agent.tool_use", "agent.mcp_tool_use":
			var payload struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(ev.Payload, &payload) == nil && payload.ID != "" {
				modelToEvent[payload.ID] = ev.ID
			}
		case "agent.tool_result", "agent.mcp_tool_result":
			var payload struct {
				ToolUseID      string `json:"tool_use_id"`
				ToolUseEventID string `json:"tool_use_event_id"`
			}
			if json.Unmarshal(ev.Payload, &payload) != nil {
				continue
			}
			if payload.ToolUseEventID != "" {
				resolved[payload.ToolUseEventID] = true
				continue
			}
			if eventID := modelToEvent[payload.ToolUseID]; eventID != "" {
				resolved[eventID] = true
			}
		}
	}
	return resolved
}

func toolResultEventType(toolUseEventType string) string {
	if toolUseEventType == "agent.mcp_tool_use" {
		return "agent.mcp_tool_result"
	}
	return "agent.tool_result"
}

func (h *Harness) executeServerTool(ctx context.Context, sb sandbox.Sandbox, mcpSess *mcpSession, name string, input json.RawMessage) (string, bool) {
	if mcpSess.isMCPTool(name) {
		return mcpSess.call(ctx, name, input)
	}
	t, ok := h.Tools.Get(name)
	if !ok {
		return "unknown tool: " + name, true
	}
	res, err := t.Execute(ctx, sb, input)
	if err != nil {
		return err.Error(), true
	}
	return res.Content, res.IsError
}

func (h *Harness) publishToolResult(ctx context.Context, sessionID, threadID, resultType, modelToolUseID, toolUseEventID, content string, isErr bool) {
	resPayload, _ := json.Marshal(map[string]any{
		"type":              resultType,
		"tool_use_id":       modelToolUseID,
		"tool_use_event_id": toolUseEventID,
		"content":           content,
		"is_error":          isErr,
	})
	_, _ = h.Broker.Publish(ctx, sessionID, resultType, threadID, resPayload)
}

func requiresActionIdlePayload(eventIDs []string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"type": "session.status_idle",
		"stop_reason": map[string]any{
			"type":      "requires_action",
			"event_ids": eventIDs,
			"requires_action": map[string]any{
				"event_ids": eventIDs,
			},
		},
	})
	return payload
}
