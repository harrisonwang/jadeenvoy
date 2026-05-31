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

// ErrConflict 用于唯一约束冲突（如同 host 已有活跃凭据）。
var ErrConflict = errors.New("conflict")

// ─── Vault ──────────────────────────────────────────────────────────────────

type VaultRow struct {
	ID          string
	TenantID    string
	DisplayName string
	Metadata    json.RawMessage
	ArchivedAt  sql.NullInt64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *Store) CreateVault(ctx context.Context, tenantID, displayName string, metadata json.RawMessage) (*VaultRow, error) {
	if tenantID == "" {
		tenantID = "tnt-default"
	}
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	id := NewID("vlt")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO vault (id, tenant_id, display_name, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, tenantID, displayName, string(metadata), now, now,
	); err != nil {
		return nil, fmt.Errorf("insert vault: %w", err)
	}
	return s.GetVault(ctx, tenantID, id)
}

func (s *Store) GetVault(ctx context.Context, tenantID, id string) (*VaultRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, display_name, metadata, archived_at, created_at, updated_at
		 FROM vault WHERE id = ? AND tenant_id = ?`, id, tenantID)
	r := &VaultRow{}
	var meta sql.NullString
	var createdMs, updatedMs int64
	if err := row.Scan(&r.ID, &r.TenantID, &r.DisplayName, &meta, &r.ArchivedAt, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Metadata = json.RawMessage(meta.String)
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

func (s *Store) ListVaults(ctx context.Context, tenantID string, limit int) ([]*VaultRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM vault WHERE tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VaultRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		v, err := s.GetVault(ctx, tenantID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ArchiveVault(ctx context.Context, tenantID, id string) error {
	now := time.Now().UTC().UnixMilli()
	res, err := s.DB.ExecContext(ctx, `UPDATE vault SET archived_at = ?, updated_at = ? WHERE id = ? AND tenant_id = ? AND archived_at IS NULL`, now, now, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteVault(ctx context.Context, tenantID, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM vault WHERE id = ? AND tenant_id = ?`, id, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── Vault credential ─────────────────────────────────────────────────────

type CredentialRow struct {
	ID            string
	VaultID       string
	TenantID      string
	DisplayName   string
	AuthType      string
	MCPServerURL  string
	MCPServerHost string
	Cipher        []byte
	CipherNonce   []byte
	CipherLabel   string
	ArchivedAt    sql.NullInt64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateCredentialInput struct {
	VaultID       string
	TenantID      string
	DisplayName   string
	AuthType      string
	MCPServerURL  string
	MCPServerHost string
	Cipher        []byte
	CipherNonce   []byte
	CipherLabel   string
}

// CreateCredential 插入凭据。同一 vault 同 host 已有活跃凭据时返回 ErrConflict
// （DB 层也有 partial unique index 兜底，见 ADR-0015 / oma-gaps 第 9 条）。
func (s *Store) CreateCredential(ctx context.Context, in CreateCredentialInput) (*CredentialRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("crd")
	now := time.Now().UTC().UnixMilli()

	// 应用层先查，给出友好错误（DB partial unique index 兜底防并发）。
	var existing int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM vault_credential
		 WHERE vault_id = ? AND mcp_server_host = ? AND archived_at IS NULL`,
		in.VaultID, in.MCPServerHost).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, ErrConflict
	}

	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO vault_credential (id, vault_id, tenant_id, display_name, auth_type,
		    mcp_server_url, mcp_server_host, cipher, cipher_nonce, cipher_label, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.VaultID, in.TenantID, in.DisplayName, in.AuthType,
		in.MCPServerURL, in.MCPServerHost, in.Cipher, in.CipherNonce, in.CipherLabel, now, now,
	); err != nil {
		return nil, fmt.Errorf("insert credential: %w", err)
	}
	return s.GetCredential(ctx, id)
}

func (s *Store) GetCredential(ctx context.Context, id string) (*CredentialRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, vault_id, tenant_id, display_name, auth_type, mcp_server_url, mcp_server_host,
		        cipher, cipher_nonce, cipher_label, archived_at, created_at, updated_at
		 FROM vault_credential WHERE id = ?`, id)
	return scanCredential(row)
}

func (s *Store) ListCredentialsByVault(ctx context.Context, tenantID, vaultID string) ([]*CredentialRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, vault_id, tenant_id, display_name, auth_type, mcp_server_url, mcp_server_host,
		        cipher, cipher_nonce, cipher_label, archived_at, created_at, updated_at
		 FROM vault_credential WHERE vault_id = ? AND tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC`, vaultID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CredentialRow{}
	for rows.Next() {
		r, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListActiveCredentialsByHost 返回某租户下指定 vault 集合里匹配 host 的活跃凭据，
// created_at DESC 排序（最新优先）。MITM 注入用。tenant 谓词确保即使 vaultIDs 误含
// 他租户 vault，也不会泄漏其凭据（深度防御，见 ADR-0015）。
func (s *Store) ListActiveCredentialsByHost(ctx context.Context, tenantID string, vaultIDs []string, host string) ([]*CredentialRow, error) {
	if len(vaultIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(vaultIDs))
	args := make([]any, 0, len(vaultIDs)+2)
	for i, id := range vaultIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, host, tenantID)
	q := fmt.Sprintf(
		`SELECT id, vault_id, tenant_id, display_name, auth_type, mcp_server_url, mcp_server_host,
		        cipher, cipher_nonce, cipher_label, archived_at, created_at, updated_at
		 FROM vault_credential
		 WHERE vault_id IN (%s) AND mcp_server_host = ? AND tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC`,
		strings.Join(placeholders, ", "))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CredentialRow{}
	for rows.Next() {
		r, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteCredential 删除凭据，强制校验它属于指定 vault 与租户（防跨租户/跨 vault 越权删除）。
func (s *Store) DeleteCredential(ctx context.Context, tenantID, vaultID, id string) error {
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM vault_credential WHERE id = ? AND vault_id = ? AND tenant_id = ?`, id, vaultID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner 抽象 *sql.Row 和 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...any) error
}

func scanCredential(sc scanner) (*CredentialRow, error) {
	r := &CredentialRow{}
	var createdMs, updatedMs int64
	if err := sc.Scan(&r.ID, &r.VaultID, &r.TenantID, &r.DisplayName, &r.AuthType,
		&r.MCPServerURL, &r.MCPServerHost, &r.Cipher, &r.CipherNonce, &r.CipherLabel,
		&r.ArchivedAt, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}
