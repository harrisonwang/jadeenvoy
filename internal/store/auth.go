package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ─── Users ────────────────────────────────────────────────────────────────

type UserRow struct {
	ID           string
	TenantID     string
	Email        string
	Name         string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateUserInput struct {
	TenantID     string
	Email        string
	Name         string
	PasswordHash string
}

// CreateUser 插入用户。email 已存在时返回 ErrConflict。
func (s *Store) CreateUser(ctx context.Context, in CreateUserInput) (*UserRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	var exists int
	if err := s.queryRow(ctx, `SELECT COUNT(1) FROM app_user WHERE email = ?`, in.Email).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, ErrConflict
	}
	id := NewID("usr")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, name, password_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.TenantID, in.Email, in.Name, in.PasswordHash, now, now,
	); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

func (s *Store) GetUser(ctx context.Context, id string) (*UserRow, error) {
	return scanUser(s.queryRow(ctx,
		`SELECT id, tenant_id, email, name, password_hash, created_at, updated_at
		 FROM app_user WHERE id = ?`, id))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*UserRow, error) {
	return scanUser(s.queryRow(ctx,
		`SELECT id, tenant_id, email, name, password_hash, created_at, updated_at
		 FROM app_user WHERE email = ?`, email))
}

func scanUser(sc scanner) (*UserRow, error) {
	r := &UserRow{}
	var createdMs, updatedMs int64
	if err := sc.Scan(&r.ID, &r.TenantID, &r.Email, &r.Name, &r.PasswordHash, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

// ─── Auth sessions（cookie 登录态） ───────────────────────────────────────

type AuthSessionRow struct {
	ID        string
	UserID    string
	TenantID  string
	ExpiresAt int64 // unix ms
	CreatedAt time.Time
}

func (s *Store) CreateAuthSession(ctx context.Context, id, userID, tenantID string, expiresAtMs int64) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.exec(ctx,
		`INSERT INTO auth_session (id, user_id, tenant_id, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, userID, tenantID, expiresAtMs, now)
	return err
}

func (s *Store) GetAuthSession(ctx context.Context, id string) (*AuthSessionRow, error) {
	r := &AuthSessionRow{}
	var createdMs int64
	if err := s.queryRow(ctx,
		`SELECT id, user_id, tenant_id, expires_at, created_at FROM auth_session WHERE id = ?`, id,
	).Scan(&r.ID, &r.UserID, &r.TenantID, &r.ExpiresAt, &createdMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	return r, nil
}

func (s *Store) DeleteAuthSession(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM auth_session WHERE id = ?`, id)
	return err
}

// ─── API keys ─────────────────────────────────────────────────────────────

type APIKeyRow struct {
	ID        string
	TenantID  string
	UserID    sql.NullString
	Name      string
	Prefix    string
	Hash      string
	CreatedAt time.Time
	RevokedAt sql.NullInt64
}

type CreateAPIKeyInput struct {
	TenantID string
	UserID   string
	Name     string
	Prefix   string
	Hash     string
}

func (s *Store) CreateAPIKey(ctx context.Context, in CreateAPIKeyInput) (*APIKeyRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("key")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.exec(ctx,
		`INSERT INTO api_key (id, tenant_id, user_id, name, prefix, hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.TenantID, nullStr(in.UserID), in.Name, in.Prefix, in.Hash, now,
	); err != nil {
		return nil, err
	}
	return s.GetAPIKey(ctx, id)
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (*APIKeyRow, error) {
	return scanAPIKey(s.queryRow(ctx,
		`SELECT id, tenant_id, user_id, name, prefix, hash, created_at, revoked_at
		 FROM api_key WHERE id = ?`, id))
}

// GetAPIKeyByHash 按 hash 查找未撤销的 key。
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKeyRow, error) {
	return scanAPIKey(s.queryRow(ctx,
		`SELECT id, tenant_id, user_id, name, prefix, hash, created_at, revoked_at
		 FROM api_key WHERE hash = ? AND revoked_at IS NULL`, hash))
}

func (s *Store) ListAPIKeys(ctx context.Context, tenantID string) ([]*APIKeyRow, error) {
	rows, err := s.query(ctx,
		`SELECT id, tenant_id, user_id, name, prefix, hash, created_at, revoked_at
		 FROM api_key WHERE tenant_id = ? AND revoked_at IS NULL
		 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIKeyRow{}
	for rows.Next() {
		r, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(ctx, `UPDATE api_key SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanAPIKey(sc scanner) (*APIKeyRow, error) {
	r := &APIKeyRow{}
	var createdMs int64
	if err := sc.Scan(&r.ID, &r.TenantID, &r.UserID, &r.Name, &r.Prefix, &r.Hash, &createdMs, &r.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	return r, nil
}
