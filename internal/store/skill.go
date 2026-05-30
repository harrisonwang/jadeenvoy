package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SkillRow 技能元数据。
type SkillRow struct {
	ID             string
	TenantID       string
	Name           string
	Description    string
	FilesJSON      json.RawMessage // [{path, content}]
	SkillMDContent string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SkillFileEntry 技能内的单个文件。
type SkillFileEntry struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type CreateSkillInput struct {
	TenantID    string
	Name        string
	Description string
	Files       []SkillFileEntry
}

func (s *Store) CreateSkill(ctx context.Context, in CreateSkillInput) (*SkillRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("skl")
	now := time.Now().UTC().UnixMilli()
	filesJSON, _ := json.Marshal(in.Files)
	if len(filesJSON) == 0 {
		filesJSON = []byte(`[]`)
	}

	// 提取 SKILL.md 内容
	skillMD := ""
	for _, f := range in.Files {
		if f.Path == "SKILL.md" || f.Path == "skill.md" {
			skillMD = f.Content
			break
		}
	}

	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO skill (id, tenant_id, name, description, files_json, skill_md_content, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.TenantID, in.Name, in.Description, string(filesJSON), skillMD, now, now,
	); err != nil {
		return nil, err
	}
	return s.GetSkill(ctx, id)
}

func (s *Store) GetSkill(ctx context.Context, id string) (*SkillRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, description, files_json, skill_md_content, created_at, updated_at
		 FROM skill WHERE id = ?`, id)
	r := &SkillRow{}
	var createdMs, updatedMs int64
	var desc sql.NullString
	var filesStr, skillMDStr string
	if err := row.Scan(&r.ID, &r.TenantID, &r.Name, &desc,
		&filesStr, &skillMDStr, &createdMs, &updatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Description = desc.String
	r.FilesJSON = json.RawMessage(filesStr)
	r.SkillMDContent = skillMDStr
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	return r, nil
}

func (s *Store) ListSkills(ctx context.Context, tenantID string) ([]*SkillRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, description, files_json, skill_md_content, created_at, updated_at
		 FROM skill WHERE tenant_id = ? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SkillRow{}
	for rows.Next() {
		r := &SkillRow{}
		var createdMs, updatedMs int64
		var desc sql.NullString
		var filesStr, skillMDStr string
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Name, &desc,
			&filesStr, &skillMDStr, &createdMs, &updatedMs); err != nil {
			return nil, err
		}
		r.Description = desc.String
		r.FilesJSON = json.RawMessage(filesStr)
		r.SkillMDContent = skillMDStr
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM skill WHERE id = ?`, id)
	return err
}
