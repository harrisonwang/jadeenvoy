package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// receivedWebhook 是测试用 webhook 接收器记录的单条投递。
type receivedWebhook struct {
	EventType string
	Payload   []byte
	Signature string
	Timestamp string
}

// recorder 是 httptest server，记录所有到达的 webhook。
type recorder struct {
	mu       sync.Mutex
	received []receivedWebhook
}

func newRecorder() (*httptest.Server, *recorder) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.received = append(rec.received, receivedWebhook{
			EventType: r.Header.Get("X-Webhook-Event"),
			Payload:   body,
			Signature: r.Header.Get("X-Webhook-Signature"),
			Timestamp: r.Header.Get("X-Webhook-Timestamp"),
		})
		rec.mu.Unlock()
		w.WriteHeader(200)
	}))
	return srv, rec
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.received)
}

func (r *recorder) get() []receivedWebhook {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]receivedWebhook, len(r.received))
	copy(out, r.received)
	return out
}

// TestE2E_M2_WebhookDelivery 端到端验证：
//  1. 注册一个 webhook endpoint 订阅 session.status_idle
//  2. 跑一个 session
//  3. webhook 接收器应该收到 session.status_idle 投递
//  4. HMAC 签名能用 endpoint secret 验证
func TestE2E_M2_WebhookDelivery(t *testing.T) {
	srv, mock := setupHarness(t)
	recSrv, rec := newRecorder()
	t.Cleanup(recSrv.Close)

	// 注册 webhook endpoint
	var ep map[string]any
	code := postJSON(t, srv, "/admin/webhooks", map[string]any{
		"url":         recSrv.URL,
		"event_types": []string{"session.status_idle"},
	}, &ep)
	if code != 201 {
		t.Fatalf("create webhook endpoint: %d, body=%v", code, ep)
	}
	secret, _ := ep["signing_secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("expected whsec_ prefix, got %q", secret)
	}

	// 简单 hello + idle 流程
	mock.AppendText("hi")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "wh-agent",
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

	waitForIdle(t, srv, sessionID, 10*time.Second)

	// dispatcher 是 500ms tick，加上传递延迟，至少等 1s
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got := rec.get()
	if len(got) == 0 {
		t.Fatalf("expected at least one webhook delivery, got 0")
	}

	// 找到 session.status_idle
	var found *receivedWebhook
	for i := range got {
		if got[i].EventType == "session.status_idle" {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected session.status_idle webhook, types received: %v", typesOf(got))
	}

	// 验证 payload 形状（兼容 Anthropic webhook）
	var body map[string]any
	if err := json.Unmarshal(found.Payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if body["type"] != "event" {
		t.Fatalf("expected payload type=event, got %v", body["type"])
	}
	data, _ := body["data"].(map[string]any)
	if data["type"] != "session.status_idle" {
		t.Fatalf("expected data.type=session.status_idle, got %v", data)
	}

	// 验证签名（v1,<hex>）
	sig := strings.TrimPrefix(found.Signature, "v1,")
	ts, _ := strconv.ParseInt(found.Timestamp, 10, 64)
	expected := computeHMAC(strings.TrimPrefix(secret, "whsec_"), found.Payload, ts)
	if sig != expected {
		t.Fatalf("signature mismatch:\n  got:      %s\n  expected: %s", sig, expected)
	}

	// F-M2-013: 验证非订阅事件类型没有投递
	// session 生命周期产生了 session.status_running / agent.message 等事件，
	// 但 endpoint 只订阅了 session.status_idle，不应收到其他类型。
	for _, g := range got {
		if g.EventType != "session.status_idle" {
			t.Errorf("received unexpected event type %q — endpoint subscribed only to session.status_idle", g.EventType)
		}
	}
}

func computeHMAC(key string, body []byte, ts int64) string {
	h := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(h, "%d.", ts)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func typesOf(got []receivedWebhook) []string {
	out := make([]string, len(got))
	for i, g := range got {
		out[i] = g.EventType
	}
	return out
}
