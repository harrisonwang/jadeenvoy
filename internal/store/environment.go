package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EnvironmentRow 是 DB 中 environment 的快照。
type EnvironmentRow struct {
	ID         string
	TenantID   string
	Name       string
	Config     json.RawMessage
	ArchivedAt sql.NullInt64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) CreateEnvironment(ctx context.Context, tenantID, name string, config json.RawMessage) (*EnvironmentRow, error) {
	if tenantID == "" {
		tenantID = "tnt-default"
	}
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	id := NewID("env")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.exec(ctx,
		`INSERT INTO environment (id, tenant_id, name, config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, tenantID, name, string(config), now, now,
	); err != nil {
		return nil, fmt.Errorf("insert environment: %w", err)
	}
	return s.GetEnvironment(ctx, id)
}

// EnsureDefaultEnvironment 幂等地确保存在 id="default" 的 environment（向后兼容旧 session）。
func (s *Store) EnsureDefaultEnvironment(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		tenantID = "tnt-default"
	}
	d, err := s.ensureDialect()
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	_, err = s.exec(ctx,
		d.InsertDefaultEnvironmentSQL(),
		tenantID, now, now)
	return err
}

func (s *Store) GetEnvironment(ctx context.Context, id string) (*EnvironmentRow, error) {
	row := s.queryRow(ctx,
		`SELECT id, tenant_id, name, config, archived_at, created_at, updated_at
		 FROM environment WHERE id = ?`, id)
	return scanEnvironment(row)
}

func (s *Store) ListEnvironments(ctx context.Context, tenantID string, limit int) ([]*EnvironmentRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.query(ctx,
		`SELECT id, tenant_id, name, config, archived_at, created_at, updated_at
		 FROM environment WHERE tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EnvironmentRow{}
	for rows.Next() {
		r, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteEnvironment(ctx context.Context, id string) error {
	if id == "default" {
		return fmt.Errorf("cannot delete the default environment")
	}
	res, err := s.exec(ctx, `DELETE FROM environment WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateEnvironment(ctx context.Context, id, name string, config json.RawMessage) (*EnvironmentRow, error) {
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(ctx,
		`UPDATE environment SET name = ?, config = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`,
		name, string(config), now, id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetEnvironment(ctx, id)
}

func (s *Store) ArchiveEnvironment(ctx context.Context, id string) (*EnvironmentRow, error) {
	if id == "default" {
		return nil, fmt.Errorf("cannot archive the default environment")
	}
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(ctx,
		`UPDATE environment SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetEnvironment(ctx, id)
}

func scanEnvironment(sc scanner) (*EnvironmentRow, error) {
	r := &EnvironmentRow{}
	var cfg sql.NullString
	var createdMs, updatedMs int64
	if err := sc.Scan(&r.ID, &r.TenantID, &r.Name, &cfg, &r.ArchivedAt, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Config = json.RawMessage(cfg.String)
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}
