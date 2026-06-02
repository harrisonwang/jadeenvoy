package e2e

import (
	"strings"
	"testing"

	"github.com/harrisonwang/jadeenvoy/internal/harness"
)

// TestE2E_Compaction_Summarization 验证长会话在 turn 边界把旧历史摘要成
// agent.thread_context_compacted checkpoint（ADR-0021）。
//
// 配置：阈值压到 10 token、只保留最近 1 个 turn。发两个 user turn 后，第二个 turn
// 开始时历史已超阈值 → 触发摘要 → 写回 checkpoint 事件。
func TestE2E_Compaction_Summarization(t *testing.T) {
	srv, mock := setupHarnessWith(t, func(h *harness.Harness) {
		h.CompactThresholdTokens = 10
		h.KeepRecentTurns = 1
	})
	// 摘要轮（system 含 "compaction" 标记）→ 返回带 SUMMARY: 前缀的摘要。
	mock.AppendSummary("SUMMARY: user greeted and asked about deployment")
	// 普通对话轮 → 无条件回复。
	mock.AppendText("ack")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "compaction-agent",
		"model":  "mock-model",
		"system": "test compaction",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	// Turn 1
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "hello there, how are you doing today"}}},
		},
	}, nil)
	waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// Turn 2 —— 此时历史已超 10 token 阈值，RunTurn 开头应触发压缩。
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "second question about the deployment process please"}}},
		},
	}, nil)
	// 等 checkpoint 事件出现（compaction 仅在 turn 2 发生；不能用 waitForIdle —— 会命中 turn 1 的旧 idle）。
	events := waitForEventType(t, srv, sessionID, "agent.thread_context_compacted", 10*1000_000_000)

	// 断言 checkpoint 摘要内容来自摘要轮。
	cp := findFirstEvent(t, events, "agent.thread_context_compacted")
	summary, _ := cp["summary"].(string)
	if !strings.Contains(summary, "SUMMARY:") {
		t.Fatalf("expected compaction summary from summarizer, got %q", summary)
	}
	if _, ok := cp["through_seq"]; !ok {
		t.Fatalf("expected through_seq in checkpoint payload, got %v", cp)
	}

	// 压缩后这一 turn 仍应正常产出最终回复并回到 idle。
	assertHasEventType(t, events, "agent.message")
	assertHasEventType(t, events, "session.status_idle")
}

// TestE2E_MaxTurns_StopReason 验证撞到 MaxSteps 上限时，idle 的 stop_reason 是
// max_turns（不再撒谎成 end_turn）。
func TestE2E_MaxTurns_StopReason(t *testing.T) {
	srv, mock := setupHarnessWith(t, func(h *harness.Harness) {
		h.MaxSteps = 3
	})
	// 永远返回 tool_use → agent 永不自然结束 → 跑满 MaxSteps。
	mock.AppendAlwaysToolUse("bash", map[string]any{"command": "echo hi"})

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "loop-agent",
		"model":  "mock-model",
		"system": "test max turns",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "loop forever"}}},
		},
	}, nil)
	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	idle := findFirstEvent(t, events, "session.status_idle")
	sr, ok := idle["stop_reason"].(map[string]any)
	if !ok {
		t.Fatalf("expected stop_reason object on idle event, got %v", idle["stop_reason"])
	}
	if sr["type"] != "max_turns" {
		t.Fatalf("expected stop_reason.type=max_turns when hitting MaxSteps, got %v", sr["type"])
	}
}
