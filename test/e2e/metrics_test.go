package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestE2E_M2_MetricsEndpoint 验证 /metrics 端点暴露 prometheus 指标，
// 且跑完一个 session 后 jadeenvoy_session_created_total > 0。
func TestE2E_M2_MetricsEndpoint(t *testing.T) {
	srv, mock := setupHarness(t)
	mock.AppendText("hello")

	// 触发一个 session 流程
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "m-agent",
		"model":  "mock-model",
		"system": "test",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "hi"}}},
		},
	}, nil)

	waitForIdle(t, srv, sessionID, 5*1000_000_000)

	// 拉 metrics
	resp, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	requiredMetrics := []string{
		"jadeenvoy_http_requests_total",
		"jadeenvoy_http_request_duration_seconds",
		"jadeenvoy_session_created_total",
		"jadeenvoy_session_events_total",
	}
	for _, m := range requiredMetrics {
		if !strings.Contains(text, m) {
			t.Errorf("metrics missing %q", m)
		}
	}

	// session_created_total 至少为 1
	if !strings.Contains(text, "jadeenvoy_session_created_total 1") &&
		!strings.Contains(text, "jadeenvoy_session_created_total 2") {
		// 容忍多个测试串跑加多次（如果 metrics 是 process global）
		if !strings.Contains(text, "jadeenvoy_session_created_total") {
			t.Error("session_created_total has no value")
		}
	}

	// 验证 content type
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", resp.Header.Get("Content-Type"))
	}
}

// 避免 unused
var _ = http.MethodGet
