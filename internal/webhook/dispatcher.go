// Package webhook 是出站 webhook 投递 —— endpoint 管理 + 签名 + 重试。
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/event"
	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// Service 提供 endpoint CRUD + 投递循环。
type Service struct {
	st      *store.Store
	client  *http.Client
	mu      sync.Mutex
	stopCh  chan struct{}
	running bool

	// 钩子：测试用，正常情况留空
	OnDelivered func(eventID, endpointID string, status int)
}

func New(st *store.Store) *Service {
	return &Service{
		st:     st,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// ─── Endpoint API ─────────────────────────────────────────────────────────

type Endpoint struct {
	Type           string    `json:"type"`
	ID             string    `json:"id"`
	URL            string    `json:"url"`
	EventTypes     []string  `json:"event_types"`
	SigningSecret  string    `json:"signing_secret,omitempty"` // 仅创建时返回一次
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
}

type CreateEndpointRequest struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
}

func (s *Service) CreateEndpoint(ctx context.Context, tenantID string, req CreateEndpointRequest) (*Endpoint, error) {
	if req.URL == "" {
		return nil, errors.New("url is required")
	}
	if !(strings.HasPrefix(req.URL, "http://") || strings.HasPrefix(req.URL, "https://")) {
		return nil, errors.New("url must start with http:// or https://")
	}
	secret := "whsec_" + randomHex(32)
	row, err := s.st.CreateWebhookEndpoint(ctx, store.CreateWebhookEndpointInput{
		TenantID:      tenantID,
		URL:           req.URL,
		EventTypes:    req.EventTypes,
		SigningSecret: secret,
	})
	if err != nil {
		return nil, err
	}
	out := endpointRowToAPI(row)
	out.SigningSecret = secret // 仅这次返回明文
	return out, nil
}

func (s *Service) ListEndpoints(ctx context.Context, tenantID string) ([]*Endpoint, error) {
	rows, err := s.st.ListWebhookEndpoints(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]*Endpoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, endpointRowToAPI(r))
	}
	return out, nil
}

func (s *Service) DeleteEndpoint(ctx context.Context, id string) error {
	return s.st.DeleteWebhookEndpoint(ctx, id)
}

func endpointRowToAPI(r *store.WebhookEndpointRow) *Endpoint {
	o := &Endpoint{
		Type:      "webhook_endpoint",
		ID:        r.ID,
		URL:       r.URL,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	_ = json.Unmarshal(r.EventTypes, &o.EventTypes)
	if r.DisabledReason.Valid {
		o.DisabledReason = r.DisabledReason.String
	}
	return o
}

// ─── 事件订阅入口 ────────────────────────────────────────────────────────

// PublishEvent 把一个 broker 事件转成 webhook delivery 入队。
// 由 broker 订阅者调用。
func (s *Service) PublishEvent(ctx context.Context, tenantID string, ev event.Event) error {
	endpoints, err := s.st.ListWebhookEndpointsByEventType(ctx, tenantID, ev.Type)
	if err != nil {
		return err
	}
	for _, ep := range endpoints {
		// 构造 webhook payload（事件信息精简版，对齐 Anthropic spec）
		payload := map[string]any{
			"type":       "event",
			"id":         "evte_" + ev.ID, // wrap event id
			"created_at": time.Now().UTC().Format(time.RFC3339),
			"data": map[string]any{
				"type":       ev.Type,
				"id":         ev.SessionID,
				"session_id": ev.SessionID,
			},
		}
		body, _ := json.Marshal(payload)
		_, err := s.st.EnqueueWebhookDelivery(ctx, store.EnqueueDeliveryInput{
			EndpointID: ep.ID,
			EventID:    ev.ID,
			EventType:  ev.Type,
			Payload:    body,
		})
		if err != nil {
			obs.Logger().Warn("webhook.enqueue.failed", "endpoint_id", ep.ID, "err", err.Error())
		}
	}
	return nil
}

// ─── 投递循环 ────────────────────────────────────────────────────────────

// Run 启动后台投递循环，直到 ctx done。同进程只能跑一个。
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return
		case <-ticker.C:
			s.dispatchPending(ctx)
		}
	}
}

func (s *Service) dispatchPending(ctx context.Context) {
	deliveries, err := s.st.ClaimPendingDeliveries(ctx, 16)
	if err != nil {
		obs.Logger().Warn("webhook.claim.failed", "err", err.Error())
		return
	}
	for _, d := range deliveries {
		s.deliverOne(ctx, d)
	}
}

func (s *Service) deliverOne(ctx context.Context, d *store.WebhookDeliveryRow) {
	ep, err := s.st.GetWebhookEndpoint(ctx, d.EndpointID)
	if err != nil {
		obs.Logger().Warn("webhook.endpoint.gone", "delivery_id", d.ID, "err", err.Error())
		return
	}
	if ep.DisabledAt.Valid {
		// endpoint 已禁用，标记 delivered 跳过
		_ = s.st.MarkDeliveryDelivered(ctx, d.ID, 0)
		return
	}

	// 签名
	now := time.Now().UTC()
	signature := sign(ep.SigningSecret, d.Payload, now)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(d.Payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "JadeEnvoy-Webhook/1.0")
	req.Header.Set("X-Webhook-Event", d.EventType)
	req.Header.Set("X-Webhook-Event-Id", d.EventID)
	req.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("X-Webhook-Signature", "v1,"+signature)

	resp, err := s.client.Do(req)
	status := 0
	var bodyText string
	if resp != nil {
		status = resp.StatusCode
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		bodyText = string(b)
		resp.Body.Close()
	}

	ok := err == nil && status >= 200 && status < 300
	if ok {
		_ = s.st.MarkDeliveryDelivered(ctx, d.ID, status)
		_ = s.st.ResetWebhookFailures(ctx, ep.ID)
		obs.Logger().Info("webhook.delivered",
			"endpoint_id", ep.ID, "event_type", d.EventType, "status", status)
	} else {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		} else {
			errMsg = fmt.Sprintf("status %d: %s", status, bodyText)
		}
		nextAttempt, finalGiveUp := nextAttemptAfter(d.Attempt, time.Now())
		if finalGiveUp {
			_ = s.st.MarkDeliveryDelivered(ctx, d.ID, status) // 终止重试
			_ = s.st.IncrementWebhookFailures(ctx, ep.ID, errMsg)
			obs.Logger().Warn("webhook.gave_up",
				"endpoint_id", ep.ID, "delivery_id", d.ID, "attempts", d.Attempt+1)
		} else {
			_ = s.st.MarkDeliveryFailed(ctx, d.ID, status, errMsg, nextAttempt.UnixMilli())
			obs.Logger().Warn("webhook.failed",
				"endpoint_id", ep.ID, "attempt", d.Attempt+1, "err", errMsg)
		}
	}

	if s.OnDelivered != nil {
		s.OnDelivered(d.EventID, ep.ID, status)
	}
}

// sign 计算 HMAC-SHA256 签名（跟 Anthropic 风格兼容）。
//
// 签名内容: "<timestamp>.<body>"
func sign(secret string, body []byte, ts time.Time) string {
	key := []byte(strings.TrimPrefix(secret, "whsec_"))
	if len(key) == 0 {
		key = []byte(secret)
	}
	h := hmac.New(sha256.New, key)
	fmt.Fprintf(h, "%d.", ts.Unix())
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// nextAttemptAfter 决定下一次重试时间，达到上限返回 finalGiveUp=true。
//
// 退避: 1s, 5s, 30s, 5m, 1h。5 次后放弃。
func nextAttemptAfter(attempt int, now time.Time) (time.Time, bool) {
	delays := []time.Duration{
		1 * time.Second,
		5 * time.Second,
		30 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
	}
	if attempt >= len(delays) {
		return time.Time{}, true
	}
	return now.Add(delays[attempt]), false
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
