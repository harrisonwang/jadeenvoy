package e2e

import (
	"testing"
	"time"
)

// TestE2E_UserInterrupt 验证 user.interrupt 打断运行中的 turn，session 回到
// clean idle{stop_reason:interrupt}（不是 terminated）（ADR-0025）。
//
// 用一个会 sleep 的 bash 工具让 turn 卡在 tool 执行；中途发 interrupt。
func TestE2E_UserInterrupt(t *testing.T) {
	srv, mock := setupHarness(t)
	// LLM 每轮都调 bash sleep —— turn 会长时间停在 tool 执行，给 interrupt 留窗口。
	mock.AppendAlwaysToolUse("bash", map[string]any{"command": "sleep 3"})

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "intr-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "do slow work"}}},
		},
	}, nil)

	// 等 turn 真正开始（running）。
	waitForEventType(t, srv, sessionID, "session.status_running", 5*1000_000_000)
	time.Sleep(200 * time.Millisecond) // 确保进入 tool 执行

	// 发 interrupt。
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.interrupt"},
		},
	}, nil)

	// 应回到 idle，且 stop_reason=interrupt，绝不是 terminated。
	events := waitForIdle(t, srv, sessionID, 8*1000_000_000)
	assertHasEventType(t, events, "user.interrupt")
	for _, ev := range events {
		if ev["type"] == "session.status_terminated" {
			t.Fatal("interrupt must not produce terminated")
		}
	}
	idle := findFirstEvent(t, events, "session.status_idle")
	if sr, _ := idle["stop_reason"].(map[string]any); sr["type"] != "interrupt" {
		t.Fatalf("expected stop_reason=interrupt, got %v", idle["stop_reason"])
	}

	var checkSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &checkSess)
	if checkSess["status"] != "idle" {
		t.Fatalf("expected idle after interrupt, got %v", checkSess["status"])
	}
}
