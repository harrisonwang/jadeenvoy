package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SessionResourceRow 表示 session 关联的资源。
type SessionResourceRow struct {
	ID        string
	SessionID string
	Type      string          // memory_store / file / github_repository
	Payload   json.RawMessage // 类型相关数据
	CreatedAt time.Time
}

type AddSessionResourceInput struct {
	SessionID string
	Type      string
	Payload   json.RawMessage
}

func (s *Store) AddSessionResource(ctx context.Context, in AddSessionResourceInput) (*SessionResourceRow, error) {
	id := NewID("sres")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO session_resource (id, session_id, type, payload, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, in.SessionID, in.Type, string(in.Payload), now,
	); err != nil {
		return nil, err
	}
	return &SessionResourceRow{
		ID:        id,
		SessionID: in.SessionID,
		Type:      in.Type,
		Payload:   in.Payload,
		CreatedAt: time.UnixMilli(now).UTC(),
	}, nil
}

func (s *Store) ListSessionResources(ctx context.Context, sessionID string) ([]*SessionResourceRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, session_id, type, payload, created_at FROM session_resource
		 WHERE session_id = ? ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionResourceRow{}
	for rows.Next() {
		r := &SessionResourceRow{}
		var payload string
		var createdMs int64
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Type, &payload, &createdMs); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload)
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSessionResource(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM session_resource WHERE id = ?`, id)
	return err
}

func (s *Store) GetSessionResource(ctx context.Context, id string) (*SessionResourceRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, session_id, type, payload, created_at FROM session_resource WHERE id = ?`, id)
	r := &SessionResourceRow{}
	var payload string
	var createdMs int64
	if err := row.Scan(&r.ID, &r.SessionID, &r.Type, &payload, &createdMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Payload = json.RawMessage(payload)
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	return r, nil
}
