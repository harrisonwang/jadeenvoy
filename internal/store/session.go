package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SessionRow 是 DB 中 session 的快照。
type SessionRow struct {
	ID               string
	TenantID         string
	AgentID          string
	AgentVersion     int
	AgentSnapshot    json.RawMessage
	EnvironmentID    string
	Status           string
	Title            sql.NullString
	Metadata         json.RawMessage
	VaultIDs         json.RawMessage
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ArchivedAt       sql.NullInt64
	TerminatedAt     sql.NullInt64
	NextSeq          int64
	UsageInput       int64
	UsageOutput      int64
	UsageCacheCreate int64
	UsageCacheRead   int64
}

type CreateSessionInput struct {
	TenantID      string
	AgentID       string
	AgentVersion  int
	AgentSnapshot json.RawMessage
	EnvironmentID string
	Title         string
	VaultIDs      []string
	Metadata      json.RawMessage
}

func (s *Store) CreateSession(ctx context.Context, in CreateSessionInput) (*SessionRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	if in.EnvironmentID == "" {
		in.EnvironmentID = "default"
	}
	id := NewID("sess")
	now := time.Now().UTC().UnixMilli()
	vaultIDsJSON, _ := json.Marshal(in.VaultIDs)
	if len(vaultIDsJSON) == 0 {
		vaultIDsJSON = []byte(`[]`)
	}
	meta := in.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	if _, err := s.exec(ctx,
		`INSERT INTO session (id, tenant_id, agent_id, agent_version, agent_snapshot,
		    environment_id, status, title, metadata, vault_ids, created_at, updated_at, next_seq)
		 VALUES (?, ?, ?, ?, ?, ?, 'idle', ?, ?, ?, ?, ?, 1)`,
		id, in.TenantID, in.AgentID, in.AgentVersion, string(in.AgentSnapshot),
		in.EnvironmentID, nullStr(in.Title), string(meta), string(vaultIDsJSON),
		now, now,
	); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return s.GetSession(ctx, id)
}

func (s *Store) GetSession(ctx context.Context, id string) (*SessionRow, error) {
	row := s.queryRow(ctx,
		`SELECT id, tenant_id, agent_id, agent_version, agent_snapshot,
		        environment_id, status, title, metadata, vault_ids,
		        created_at, updated_at, archived_at, terminated_at, next_seq,
		        usage_input_tokens, usage_output_tokens, usage_cache_create_tokens, usage_cache_read_tokens
		 FROM session WHERE id = ?`, id)
	r := &SessionRow{}
	var createdMs, updatedMs int64
	var snap, meta, vaultIDs sql.NullString
	if err := row.Scan(
		&r.ID, &r.TenantID, &r.AgentID, &r.AgentVersion, &snap,
		&r.EnvironmentID, &r.Status, &r.Title, &meta, &vaultIDs,
		&createdMs, &updatedMs, &r.ArchivedAt, &r.TerminatedAt, &r.NextSeq,
		&r.UsageInput, &r.UsageOutput, &r.UsageCacheCreate, &r.UsageCacheRead,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	r.AgentSnapshot = json.RawMessage(snap.String)
	r.Metadata = json.RawMessage(meta.String)
	r.VaultIDs = json.RawMessage(vaultIDs.String)
	return r, nil
}

func (s *Store) UpdateSessionStatus(ctx context.Context, id, status string) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.exec(ctx,
		`UPDATE session SET status = ?, updated_at = ? WHERE id = ?`,
		status, now, id)
	return err
}

// ListSessionsByStatus 返回某状态下的所有 session id（不分租户，用于启动恢复）。
func (s *Store) ListSessionsByStatus(ctx context.Context, statuses ...string) ([]string, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(statuses))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.query(ctx,
		`SELECT id FROM session WHERE status IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkSessionTerminated 置 session 为 terminated 并记录 terminated_at（ADR-0022）。
func (s *Store) MarkSessionTerminated(ctx context.Context, id string) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.exec(ctx,
		`UPDATE session SET status = 'terminated', terminated_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id)
	return err
}

func (s *Store) UpdateSessionUsage(ctx context.Context, id string, input, output, cacheCreate, cacheRead int64) error {
	_, err := s.exec(ctx,
		`UPDATE session SET
		    usage_input_tokens = usage_input_tokens + ?,
		    usage_output_tokens = usage_output_tokens + ?,
		    usage_cache_create_tokens = usage_cache_create_tokens + ?,
		    usage_cache_read_tokens = usage_cache_read_tokens + ?
		 WHERE id = ?`,
		input, output, cacheCreate, cacheRead, id)
	return err
}

func (s *Store) UpdateSession(ctx context.Context, id, title string, metadata json.RawMessage, vaultIDs []string) (*SessionRow, error) {
	now := time.Now().UTC().UnixMilli()
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	vaultIDsJSON, _ := json.Marshal(vaultIDs)
	if len(vaultIDsJSON) == 0 {
		vaultIDsJSON = []byte(`[]`)
	}
	res, err := s.exec(ctx,
		`UPDATE session SET title = ?, metadata = ?, vault_ids = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`,
		nullStr(title), string(metadata), string(vaultIDsJSON), now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetSession(ctx, id)
}

func (s *Store) ArchiveSession(ctx context.Context, id string) error {
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(ctx, `UPDATE session SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSessions(ctx context.Context, tenantID string, limit int) ([]*SessionRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.query(ctx,
		`SELECT id FROM session WHERE tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ss, err := s.GetSession(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM session WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
