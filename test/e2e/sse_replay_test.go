package e2e

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// readSSEUntil 读 SSE 流，收集 event: 行，直到出现 wantType 或超时。
func readSSEUntil(t *testing.T, srv *httptest.Server, path, wantType string, timeout time.Duration) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		// 超时是合法结果（流在等未来事件）——返回已收集的，由调用方判断。
		return nil
	}
	defer resp.Body.Close()

	var types []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			et := strings.TrimPrefix(line, "event: ")
			types = append(types, et)
			if et == wantType {
				return types
			}
		}
	}
	return types
}

// TestE2E_SSE_ReplayAfterIdle 验证：turn 跑完（idle 已落历史）之后才连 SSE 流，
// 客户端仍能收到 idle —— 不再死锁（P0①，对齐官方"无回放会 deadlock"的警告）。
func TestE2E_SSE_ReplayAfterIdle(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendText("hi there")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "sse-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)
	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	// 发消息并等到 turn 完成（idle 已在历史里）。
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "hi"}}},
		},
	}, nil)
	waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 现在才连 SSE —— 必须靠回放拿到历史里的 idle，否则永久阻塞。
	types := readSSEUntil(t, srv, "/v1/sessions/"+sessionID+"/events/stream", "session.status_idle", 5*time.Second)
	found := false
	for _, ty := range types {
		if ty == "session.status_idle" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected replayed session.status_idle on late connect, got %v", types)
	}
	// 回放应包含先前的 user.message / agent.message。
	if !contains(types, "agent.message") {
		t.Fatalf("expected replayed agent.message, got %v", types)
	}
}

// TestE2E_SSE_SinceSkipsOld 验证 ?since=<seq> 只补发其后的事件。
func TestE2E_SSE_SinceSkipsOld(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendText("answer one")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "sse-since-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)
	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "q"}}},
		},
	}, nil)
	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 取最后一个事件的 seq 作为 since —— 之后连接应几乎无回放（仅 > since 的）。
	lastSeq := int64(0)
	for _, ev := range events {
		if s, ok := ev["seq"].(float64); ok && int64(s) > lastSeq {
			lastSeq = int64(s)
		}
	}
	// since=lastSeq → 没有 seq>lastSeq 的历史；流应不回放任何旧事件即结束或挂起到超时。
	// 这里只验证不 panic 且不把旧 idle 重发（types 不含 idle，因为 idle.seq <= lastSeq）。
	types := readSSEUntil(t, srv, "/v1/sessions/"+sessionID+"/events/stream?since="+strconv.FormatInt(lastSeq, 10), "session.status_idle", 1500*time.Millisecond)
	if contains(types, "session.status_idle") {
		t.Fatalf("since=lastSeq should not replay the old idle, got %v", types)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
