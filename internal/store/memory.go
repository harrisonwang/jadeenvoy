package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// ─── Memory Store ─────────────────────────────────────────────────────────

type MemoryStoreRow struct {
	ID          string
	TenantID    string
	Name        string
	Description sql.NullString
	ArchivedAt  sql.NullInt64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreateMemoryStoreInput struct {
	TenantID    string
	Name        string
	Description string
}

func (s *Store) CreateMemoryStore(ctx context.Context, in CreateMemoryStoreInput) (*MemoryStoreRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("mst")
	now := time.Now().UTC().UnixMilli()
	if _, err := s.exec(ctx,
		`INSERT INTO memory_store (id, tenant_id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, in.TenantID, in.Name, nullStr(in.Description), now, now,
	); err != nil {
		return nil, err
	}
	return s.GetMemoryStore(ctx, id)
}

func (s *Store) GetMemoryStore(ctx context.Context, id string) (*MemoryStoreRow, error) {
	row := s.queryRow(ctx,
		`SELECT id, tenant_id, name, description, archived_at, created_at, updated_at
		 FROM memory_store WHERE id = ?`, id)
	r := &MemoryStoreRow{}
	var createdMs, updatedMs int64
	if err := row.Scan(&r.ID, &r.TenantID, &r.Name, &r.Description, &r.ArchivedAt, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

func (s *Store) ListMemoryStores(ctx context.Context, tenantID string, limit int) ([]*MemoryStoreRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.query(ctx,
		`SELECT id FROM memory_store WHERE tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MemoryStoreRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		r, err := s.GetMemoryStore(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) DeleteMemoryStore(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM memory_store WHERE id = ?`, id)
	return err
}

func (s *Store) UpdateMemoryStore(ctx context.Context, id, name, description string) (*MemoryStoreRow, error) {
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(ctx,
		`UPDATE memory_store SET name = ?, description = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`,
		name, nullStr(description), now, id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetMemoryStore(ctx, id)
}

func (s *Store) ArchiveMemoryStore(ctx context.Context, id string) (*MemoryStoreRow, error) {
	now := time.Now().UTC().UnixMilli()
	res, err := s.exec(ctx,
		`UPDATE memory_store SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetMemoryStore(ctx, id)
}

// ─── Memory (单条) ─────────────────────────────────────────────────────────

type MemoryRow struct {
	ID            string
	MemoryStoreID string
	TenantID      string
	Path          string
	Content       string
	ContentSha256 string
	ContentSize   int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateMemoryInput struct {
	MemoryStoreID string
	TenantID      string
	Path          string
	Content       string
}

type UpdateMemoryInput struct {
	ID      string
	Path    string
	Content *string
}

type MemoryVersionRow struct {
	ID            string
	MemoryStoreID string
	MemoryID      string
	TenantID      string
	Operation     string
	Path          sql.NullString
	Content       sql.NullString
	ContentSha256 sql.NullString
	ContentSize   sql.NullInt64
	CreatedAt     time.Time
	RedactedAt    sql.NullInt64
}

func (s *Store) CreateMemory(ctx context.Context, in CreateMemoryInput) (*MemoryRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("mem")
	now := time.Now().UTC().UnixMilli()
	sum := sha256Hex(in.Content)
	if _, err := s.exec(ctx,
		`INSERT INTO memory (id, memory_store_id, tenant_id, path, content, content_sha256, content_size, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.MemoryStoreID, in.TenantID, in.Path, in.Content, sum, int64(len(in.Content)), now, now,
	); err != nil {
		return nil, err
	}
	if err := s.insertMemoryVersion(ctx, MemoryVersionInput{
		MemoryStoreID: in.MemoryStoreID,
		MemoryID:      id,
		TenantID:      in.TenantID,
		Operation:     "created",
		Path:          in.Path,
		Content:       in.Content,
		ContentSha256: sum,
		ContentSize:   int64(len(in.Content)),
		CreatedAtMS:   now,
	}); err != nil {
		return nil, err
	}
	return s.GetMemory(ctx, id)
}

func (s *Store) GetMemory(ctx context.Context, id string) (*MemoryRow, error) {
	row := s.queryRow(ctx,
		`SELECT id, memory_store_id, tenant_id, path, content, content_sha256, content_size, created_at, updated_at
		 FROM memory WHERE id = ?`, id)
	r := &MemoryRow{}
	var createdMs, updatedMs int64
	if err := row.Scan(&r.ID, &r.MemoryStoreID, &r.TenantID, &r.Path, &r.Content, &r.ContentSha256, &r.ContentSize, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

// UpsertMemory 按 (store_id, path) 主键 upsert。
func (s *Store) UpsertMemory(ctx context.Context, in CreateMemoryInput) (*MemoryRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	now := time.Now().UTC().UnixMilli()
	sum := sha256Hex(in.Content)

	var existing string
	err := s.queryRow(ctx,
		`SELECT id FROM memory WHERE memory_store_id = ? AND path = ?`,
		in.MemoryStoreID, in.Path,
	).Scan(&existing)
	if err == sql.ErrNoRows {
		return s.CreateMemory(ctx, in)
	}
	if err != nil {
		return nil, err
	}
	_, err = s.exec(ctx,
		`UPDATE memory SET content = ?, content_sha256 = ?, content_size = ?, updated_at = ? WHERE id = ?`,
		in.Content, sum, int64(len(in.Content)), now, existing,
	)
	if err != nil {
		return nil, err
	}
	before, err := s.GetMemory(ctx, existing)
	if err != nil {
		return nil, err
	}
	if err := s.insertMemoryVersion(ctx, MemoryVersionInput{
		MemoryStoreID: before.MemoryStoreID,
		MemoryID:      before.ID,
		TenantID:      before.TenantID,
		Operation:     "modified",
		Path:          before.Path,
		Content:       in.Content,
		ContentSha256: sum,
		ContentSize:   int64(len(in.Content)),
		CreatedAtMS:   now,
	}); err != nil {
		return nil, err
	}
	return s.GetMemory(ctx, existing)
}

func (s *Store) UpdateMemory(ctx context.Context, in UpdateMemoryInput) (*MemoryRow, error) {
	current, err := s.GetMemory(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Path == "" {
		in.Path = current.Path
	}
	content := current.Content
	if in.Content != nil {
		content = *in.Content
	}
	sum := sha256Hex(content)
	now := time.Now().UTC().UnixMilli()
	if _, err := s.exec(ctx,
		`UPDATE memory SET path = ?, content = ?, content_sha256 = ?, content_size = ?, updated_at = ? WHERE id = ?`,
		in.Path, content, sum, int64(len(content)), now, in.ID,
	); err != nil {
		return nil, err
	}
	if err := s.insertMemoryVersion(ctx, MemoryVersionInput{
		MemoryStoreID: current.MemoryStoreID,
		MemoryID:      current.ID,
		TenantID:      current.TenantID,
		Operation:     "modified",
		Path:          in.Path,
		Content:       content,
		ContentSha256: sum,
		ContentSize:   int64(len(content)),
		CreatedAtMS:   now,
	}); err != nil {
		return nil, err
	}
	return s.GetMemory(ctx, in.ID)
}

func (s *Store) ListMemories(ctx context.Context, storeID, pathPrefix string, limit int) ([]*MemoryRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if pathPrefix != "" {
		rows, err = s.query(ctx,
			`SELECT id FROM memory WHERE memory_store_id = ? AND path LIKE ? ORDER BY path ASC LIMIT ?`,
			storeID, pathPrefix+"%", limit)
	} else {
		rows, err = s.query(ctx,
			`SELECT id FROM memory WHERE memory_store_id = ? ORDER BY path ASC LIMIT ?`,
			storeID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MemoryRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		m, err := s.GetMemory(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	current, err := s.GetMemory(ctx, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if err := s.insertMemoryVersion(ctx, MemoryVersionInput{
		MemoryStoreID: current.MemoryStoreID,
		MemoryID:      current.ID,
		TenantID:      current.TenantID,
		Operation:     "deleted",
		Path:          current.Path,
		Content:       current.Content,
		ContentSha256: current.ContentSha256,
		ContentSize:   current.ContentSize,
		CreatedAtMS:   now,
	}); err != nil {
		return err
	}
	_, err = s.exec(ctx, `DELETE FROM memory WHERE id = ?`, id)
	return err
}

type MemoryVersionInput struct {
	MemoryStoreID string
	MemoryID      string
	TenantID      string
	Operation     string
	Path          string
	Content       string
	ContentSha256 string
	ContentSize   int64
	CreatedAtMS   int64
}

func (s *Store) insertMemoryVersion(ctx context.Context, in MemoryVersionInput) error {
	id := NewID("mver")
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	if in.CreatedAtMS == 0 {
		in.CreatedAtMS = time.Now().UTC().UnixMilli()
	}
	_, err := s.exec(ctx,
		`INSERT INTO memory_version
		 (id, memory_store_id, memory_id, tenant_id, operation, path, content, content_sha256, content_size, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.MemoryStoreID, in.MemoryID, in.TenantID, in.Operation, in.Path, in.Content, in.ContentSha256, in.ContentSize, in.CreatedAtMS,
	)
	return err
}

func (s *Store) ListMemoryVersions(ctx context.Context, storeID, memoryID string, limit int) ([]*MemoryVersionRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if memoryID != "" {
		rows, err = s.query(ctx,
			`SELECT id FROM memory_version WHERE memory_store_id = ? AND memory_id = ?
			 ORDER BY created_at DESC LIMIT ?`,
			storeID, memoryID, limit)
	} else {
		rows, err = s.query(ctx,
			`SELECT id FROM memory_version WHERE memory_store_id = ?
			 ORDER BY created_at DESC LIMIT ?`,
			storeID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MemoryVersionRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		v, err := s.GetMemoryVersion(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetMemoryVersion(ctx context.Context, id string) (*MemoryVersionRow, error) {
	row := s.queryRow(ctx,
		`SELECT id, memory_store_id, memory_id, tenant_id, operation,
		        path, content, content_sha256, content_size, created_at, redacted_at
		 FROM memory_version WHERE id = ?`, id)
	r := &MemoryVersionRow{}
	var createdMs int64
	if err := row.Scan(
		&r.ID, &r.MemoryStoreID, &r.MemoryID, &r.TenantID, &r.Operation,
		&r.Path, &r.Content, &r.ContentSha256, &r.ContentSize, &createdMs, &r.RedactedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	return r, nil
}

func (s *Store) RedactMemoryVersion(ctx context.Context, id string) (*MemoryVersionRow, error) {
	now := time.Now().UTC().UnixMilli()
	_, err := s.exec(ctx,
		`UPDATE memory_version
		 SET path = NULL, content = NULL, content_sha256 = NULL, content_size = NULL, redacted_at = ?
		 WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return nil, err
	}
	return s.GetMemoryVersion(ctx, id)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 防 unused
var _ = json.Marshal
