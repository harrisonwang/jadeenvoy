package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// compactionEventType 是写回 event log 的 checkpoint 事件类型，对齐 Anthropic spec。
const compactionEventType = "agent.thread_context_compacted"

// compactionSystemPrompt 是摘要调用的 system prompt。mock provider 靠其中的
// "compaction" 标记识别这是压缩轮（见 ADR-0021）。
const compactionSystemPrompt = "You are a conversation-history compaction assistant. " +
	"Summarize the prior conversation faithfully and concisely, preserving the user's goals, " +
	"key decisions, file paths, and any state needed to continue the task. Output only the summary."

// estimateTokens 粗估 messages 的 token 数（字符数/4）。仅用于触发阈值判断，不求精确。
func estimateTokens(msgs []provider.Message) int {
	chars := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			chars += len(b.Text) + len(b.ToolInput) + len(b.ToolResult)
		}
	}
	return chars / 4
}

// maybeCompact 在 token 预算超限时，于 turn 边界把旧历史摘要成一个
// agent.thread_context_compacted checkpoint 事件写回 event log（见 ADR-0021）。
// 压缩本身是一条持久事件，因此完全保持 "event log 是真相源" 不变量。
func (h *Harness) maybeCompact(ctx context.Context, sessionID, threadID, modelID string) error {
	if h.CompactThresholdTokens <= 0 {
		return nil // 关闭
	}
	keepRecent := h.KeepRecentTurns
	if keepRecent <= 0 {
		keepRecent = 3
	}

	msgs, err := h.buildMessages(ctx, sessionID, threadID)
	if err != nil {
		return err
	}
	if estimateTokens(msgs) <= h.CompactThresholdTokens {
		return nil
	}

	events, err := h.Store.ListEvents(ctx, sessionID, nil)
	if err != nil {
		return err
	}
	events = filterThreadEvents(events, threadID)

	// 找上一个 checkpoint：其 through_seq 之前已被摘要，不重复处理。
	priorThrough := int64(-1)
	priorSummary := ""
	maxSeq := int64(-1)
	for _, ev := range events {
		if ev.Type != compactionEventType || ev.Seq <= maxSeq {
			continue
		}
		var p compactionPayload
		if json.Unmarshal(ev.Payload, &p) == nil {
			maxSeq = ev.Seq
			priorThrough = p.ThroughSeq
			priorSummary = p.Summary
		}
	}

	// 收集 priorThrough 之后的 user.message 边界（user turn 起点）。
	var boundaries []int64
	for _, ev := range events {
		if ev.Seq > priorThrough && ev.Type == "user.message" {
			boundaries = append(boundaries, ev.Seq)
		}
	}
	// 至少要有 keepRecent+1 个 turn 才能安全压缩（保留最近 keepRecent 个逐字不动）。
	if len(boundaries) < keepRecent+1 {
		return nil
	}
	keepFromSeq := boundaries[len(boundaries)-keepRecent]
	throughSeq := keepFromSeq - 1

	transcript := renderTranscript(events, priorThrough, keepFromSeq)
	if transcript == "" {
		return nil
	}
	material := transcript
	if priorSummary != "" {
		material = "Summary of earlier conversation:\n" + priorSummary + "\n\nNewer conversation:\n" + transcript
	}

	summary, err := h.summarize(ctx, modelID, material)
	if err != nil {
		return err
	}
	if summary == "" {
		return nil
	}

	payload, _ := json.Marshal(compactionPayload{
		Type:       compactionEventType,
		Summary:    summary,
		ThroughSeq: throughSeq,
	})
	_, err = h.Broker.Publish(ctx, sessionID, compactionEventType, threadID, payload)
	return err
}

type compactionPayload struct {
	Type       string `json:"type,omitempty"`
	Summary    string `json:"summary"`
	ThroughSeq int64  `json:"through_seq"`
}

// renderTranscript 把 (after, before) 开区间内的对话事件渲染成纯文本喂给摘要。
func renderTranscript(events []*store.EventRow, after, before int64) string {
	var sb strings.Builder
	for _, ev := range events {
		if ev.Seq <= after || ev.Seq >= before {
			continue
		}
		switch ev.Type {
		case "user.message":
			sb.WriteString("User: ")
			sb.WriteString(extractText(ev.Payload))
			sb.WriteString("\n")
		case "agent.message":
			sb.WriteString("Assistant: ")
			sb.WriteString(extractText(ev.Payload))
			sb.WriteString("\n")
		case "agent.tool_use", "agent.custom_tool_use":
			sb.WriteString("Assistant called tool ")
			sb.WriteString(payloadString(ev.Payload, "name"))
			sb.WriteString("\n")
		case "agent.tool_result", "user.custom_tool_result":
			sb.WriteString("Tool result: ")
			sb.WriteString(payloadString(ev.Payload, "content"))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// summarize 用 compaction system prompt 调一次 provider，收集纯文本摘要。
func (h *Harness) summarize(ctx context.Context, modelID, material string) (string, error) {
	req := provider.ChatRequest{
		Model:  modelID,
		System: compactionSystemPrompt,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.ContentBlock{{Type: "text", Text: material}},
		}},
		MaxTokens: 1024,
	}
	ch, err := h.Provider.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	for ev := range ch {
		switch e := ev.(type) {
		case provider.TextDelta:
			buf.WriteString(e.Text)
		case provider.StopReason:
			return strings.TrimSpace(buf.String()), nil
		case provider.ErrorEvent:
			return "", fmt.Errorf("summarize: %s — %s", e.Type, e.Message)
		}
	}
	return strings.TrimSpace(buf.String()), nil
}

// extractText 抽取 {content:[{type:text,text}]} 里的全部文本。
func extractText(payload json.RawMessage) string {
	var p struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(payload, &p)
	var sb strings.Builder
	for _, b := range p.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// payloadString 取 payload 顶层某个 string 字段。
func payloadString(payload json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(payload, &m) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}
