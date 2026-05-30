package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type EventRow struct {
	ID          string
	SessionID   string
	ThreadID    string
	Seq         int64
	Type        string
	Payload     json.RawMessage
	ProcessedAt sql.NullInt64
	CreatedAt   time.Time
}

type AppendEventInput struct {
	SessionID string
	ThreadID  string
	Type      string
	Payload   json.RawMessage
}

// AppendEvent 追加事件，自动分配 seq（基于 session 的 next_seq）。
func (s *Store) AppendEvent(ctx context.Context, in AppendEventInput) (*EventRow, error) {
	if in.ThreadID == "" {
		in.ThreadID = "primary"
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 拿当前 next_seq + 自增
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT next_seq FROM session WHERE id = ?`, in.SessionID,
	).Scan(&seq); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE session SET next_seq = next_seq + 1 WHERE id = ?`, in.SessionID,
	); err != nil {
		return nil, err
	}

	id := NewID("evt")
	now := time.Now().UTC().UnixMilli()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO session_event (id, session_id, thread_id, seq, type, payload, processed_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.SessionID, in.ThreadID, seq, in.Type, string(in.Payload), now, now,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &EventRow{
		ID:          id,
		SessionID:   in.SessionID,
		ThreadID:    in.ThreadID,
		Seq:         seq,
		Type:        in.Type,
		Payload:     in.Payload,
		ProcessedAt: sql.NullInt64{Int64: now, Valid: true},
		CreatedAt:   time.UnixMilli(now).UTC(),
	}, nil
}

// ListEvents 按 seq 升序返回 session 的事件。
func (s *Store) ListEvents(ctx context.Context, sessionID string, types []string) ([]*EventRow, error) {
	var rows *sql.Rows
	var err error
	if len(types) == 0 {
		rows, err = s.DB.QueryContext(ctx,
			`SELECT id, session_id, thread_id, seq, type, payload, processed_at, created_at
			 FROM session_event WHERE session_id = ? ORDER BY seq ASC`,
			sessionID)
	} else {
		// SQLite 不支持 ANY/= ANY，用 IN 拼
		args := []any{sessionID}
		placeholders := ""
		for i, t := range types {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, t)
		}
		q := `SELECT id, session_id, thread_id, seq, type, payload, processed_at, created_at
		      FROM session_event WHERE session_id = ? AND type IN (` + placeholders + `)
		      ORDER BY seq ASC`
		rows, err = s.DB.QueryContext(ctx, q, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*EventRow{}
	for rows.Next() {
		r := &EventRow{}
		var payload sql.NullString
		var createdMs int64
		if err := rows.Scan(&r.ID, &r.SessionID, &r.ThreadID, &r.Seq, &r.Type, &payload, &r.ProcessedAt, &createdMs); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload.String)
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrNotFound 是 store 层"找不到"的标准错误。
var ErrNotFound = errors.New("not found")
