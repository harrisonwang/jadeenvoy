package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// FileRow 文件元数据。
type FileRow struct {
	ID          string
	TenantID    string
	Filename    string
	ContentType string
	Blob        []byte
	Size        int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreateFileInput struct {
	TenantID    string
	Filename    string
	ContentType string
	Blob        []byte
}

func (s *Store) CreateFile(ctx context.Context, in CreateFileInput) (*FileRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	if in.ContentType == "" {
		in.ContentType = "application/octet-stream"
	}
	id := NewID("fil")
	now := time.Now().UTC().UnixMilli()
	size := int64(len(in.Blob))
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO file (id, tenant_id, filename, content_type, blob, size, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.TenantID, in.Filename, in.ContentType, in.Blob, size, now, now,
	); err != nil {
		return nil, err
	}
	return s.GetFile(ctx, id)
}

func (s *Store) GetFile(ctx context.Context, id string) (*FileRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, filename, content_type, blob, size, created_at, updated_at
		 FROM file WHERE id = ?`, id)
	r := &FileRow{}
	var createdMs, updatedMs int64
	if err := row.Scan(&r.ID, &r.TenantID, &r.Filename, &r.ContentType,
		&r.Blob, &r.Size, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

func (s *Store) ListFiles(ctx context.Context, tenantID string) ([]*FileRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, filename, content_type, blob, size, created_at, updated_at
		 FROM file WHERE tenant_id = ? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*FileRow{}
	for rows.Next() {
		r := &FileRow{}
		var createdMs, updatedMs int64
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Filename, &r.ContentType,
			&r.Blob, &r.Size, &createdMs, &updatedMs); err != nil {
			return nil, err
		}
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteFile(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM file WHERE id = ?`, id)
	return err
}
