package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ─── Webhook endpoint ─────────────────────────────────────────────────────

type WebhookEndpointRow struct {
	ID                   string
	TenantID             string
	URL                  string
	EventTypes           json.RawMessage // ["session.status_idled", ...]
	SigningSecret        string
	DisabledAt           sql.NullInt64
	DisabledReason       sql.NullString
	ConsecutiveFailures  int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CreateWebhookEndpointInput struct {
	TenantID      string
	URL           string
	EventTypes    []string
	SigningSecret string
}

func (s *Store) CreateWebhookEndpoint(ctx context.Context, in CreateWebhookEndpointInput) (*WebhookEndpointRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("whk")
	now := time.Now().UTC().UnixMilli()
	types, _ := json.Marshal(in.EventTypes)
	if len(types) == 0 || string(types) == "null" {
		types = []byte(`[]`)
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO webhook_endpoint (id, tenant_id, url, event_types, signing_secret, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.TenantID, in.URL, string(types), in.SigningSecret, now, now,
	); err != nil {
		return nil, err
	}
	return s.GetWebhookEndpoint(ctx, id)
}

func (s *Store) GetWebhookEndpoint(ctx context.Context, id string) (*WebhookEndpointRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, url, event_types, signing_secret, disabled_at, disabled_reason,
		        consecutive_failures, created_at, updated_at
		 FROM webhook_endpoint WHERE id = ?`, id)
	r := &WebhookEndpointRow{}
	var types sql.NullString
	var createdMs, updatedMs int64
	if err := row.Scan(&r.ID, &r.TenantID, &r.URL, &types, &r.SigningSecret,
		&r.DisabledAt, &r.DisabledReason, &r.ConsecutiveFailures, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.EventTypes = json.RawMessage(types.String)
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

func (s *Store) ListWebhookEndpointsByEventType(ctx context.Context, tenantID, eventType string) ([]*WebhookEndpointRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM webhook_endpoint
		 WHERE tenant_id = ? AND disabled_at IS NULL`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WebhookEndpointRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ep, err := s.GetWebhookEndpoint(ctx, id)
		if err != nil {
			return nil, err
		}
		// 过滤订阅类型
		var subs []string
		_ = json.Unmarshal(ep.EventTypes, &subs)
		matched := len(subs) == 0 // 空 = 订阅所有
		for _, t := range subs {
			if t == eventType {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, ep)
		}
	}
	return out, rows.Err()
}

func (s *Store) ListWebhookEndpoints(ctx context.Context, tenantID string) ([]*WebhookEndpointRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM webhook_endpoint WHERE tenant_id = ? ORDER BY created_at DESC`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WebhookEndpointRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ep, err := s.GetWebhookEndpoint(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWebhookEndpoint(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM webhook_endpoint WHERE id = ?`, id)
	return err
}

func (s *Store) IncrementWebhookFailures(ctx context.Context, id string, lastErr string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE webhook_endpoint SET consecutive_failures = consecutive_failures + 1 WHERE id = ?`,
		id)
	return err
}

func (s *Store) ResetWebhookFailures(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE webhook_endpoint SET consecutive_failures = 0 WHERE id = ?`,
		id)
	return err
}

func (s *Store) DisableWebhookEndpoint(ctx context.Context, id, reason string) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.DB.ExecContext(ctx,
		`UPDATE webhook_endpoint SET disabled_at = ?, disabled_reason = ? WHERE id = ?`,
		now, reason, id)
	return err
}

// ─── Webhook delivery (待投递队列) ────────────────────────────────────────

type WebhookDeliveryRow struct {
	ID            string
	EndpointID    string
	EventID       string
	EventType     string
	Payload       json.RawMessage
	Attempt       int
	NextAttemptAt sql.NullInt64
	DeliveredAt   sql.NullInt64
	LastStatus    sql.NullInt64
	LastError     sql.NullString
	CreatedAt     time.Time
}

type EnqueueDeliveryInput struct {
	EndpointID string
	EventID    string
	EventType  string
	Payload    json.RawMessage
}

func (s *Store) EnqueueWebhookDelivery(ctx context.Context, in EnqueueDeliveryInput) (*WebhookDeliveryRow, error) {
	id := NewID("whd")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO webhook_delivery (id, endpoint_id, event_id, event_type, payload, next_attempt_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.EndpointID, in.EventID, in.EventType, string(in.Payload), now, now,
	); err != nil {
		return nil, err
	}
	return &WebhookDeliveryRow{
		ID:            id,
		EndpointID:    in.EndpointID,
		EventID:       in.EventID,
		EventType:     in.EventType,
		Payload:       in.Payload,
		NextAttemptAt: sql.NullInt64{Int64: now, Valid: true},
		CreatedAt:     time.UnixMilli(now).UTC(),
	}, nil
}

// ClaimPendingDeliveries 锁定一批 next_attempt_at <= now 且未投递的 deliveries。
// 简单版：每次直接 select，应用层处理并发（V1 单实例不冲突）。
func (s *Store) ClaimPendingDeliveries(ctx context.Context, limit int) ([]*WebhookDeliveryRow, error) {
	if limit <= 0 {
		limit = 16
	}
	now := time.Now().UTC().UnixMilli()
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, endpoint_id, event_id, event_type, payload, attempt, next_attempt_at, created_at
		 FROM webhook_delivery
		 WHERE delivered_at IS NULL AND next_attempt_at <= ?
		 ORDER BY next_attempt_at ASC LIMIT ?`,
		now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WebhookDeliveryRow{}
	for rows.Next() {
		r := &WebhookDeliveryRow{}
		var payload sql.NullString
		var createdMs int64
		if err := rows.Scan(&r.ID, &r.EndpointID, &r.EventID, &r.EventType,
			&payload, &r.Attempt, &r.NextAttemptAt, &createdMs); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload.String)
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MarkDeliveryDelivered(ctx context.Context, id string, status int) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.DB.ExecContext(ctx,
		`UPDATE webhook_delivery SET delivered_at = ?, last_status = ? WHERE id = ?`,
		now, status, id)
	return err
}

func (s *Store) MarkDeliveryFailed(ctx context.Context, id string, status int, errMsg string, nextAttemptAt int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE webhook_delivery SET attempt = attempt + 1, last_status = ?, last_error = ?,
		 next_attempt_at = ? WHERE id = ?`,
		status, errMsg, nextAttemptAt, id)
	return err
}
