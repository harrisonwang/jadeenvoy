package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AgentRow 是 agent + 当前 version 合并视图。
type AgentRow struct {
	ID             string
	TenantID       string
	Name           string
	Version        int
	Model          json.RawMessage
	System         sql.NullString
	Description    sql.NullString
	Tools          json.RawMessage
	MCPServers     json.RawMessage
	Skills         json.RawMessage
	Multiagent     json.RawMessage
	Metadata       json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     sql.NullInt64
	CurrentVersion int
}

type CreateAgentInput struct {
	TenantID    string
	Name        string
	Model       json.RawMessage
	System      string
	Description string
	Tools       json.RawMessage
	MCPServers  json.RawMessage
	Skills      json.RawMessage
	Metadata    json.RawMessage
}

type UpdateAgentInput struct {
	Name        string
	Model       json.RawMessage
	System      string
	Description string
	Tools       json.RawMessage
	MCPServers  json.RawMessage
	Skills      json.RawMessage
	Metadata    json.RawMessage
	Version     int
}

func (s *Store) CreateAgent(ctx context.Context, in CreateAgentInput) (*AgentRow, error) {
	if in.TenantID == "" {
		in.TenantID = "tnt-default"
	}
	id := NewID("agt")
	now := time.Now().UTC().UnixMilli()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent (id, tenant_id, name, created_at, updated_at, current_version)
		 VALUES (?, ?, ?, ?, ?, 1)`,
		id, in.TenantID, in.Name, now, now,
	); err != nil {
		return nil, fmt.Errorf("insert agent: %w", err)
	}

	tools := in.Tools
	if len(tools) == 0 {
		tools = json.RawMessage(`[]`)
	}
	mcp := in.MCPServers
	if len(mcp) == 0 {
		mcp = json.RawMessage(`[]`)
	}
	skills := in.Skills
	if len(skills) == 0 {
		skills = json.RawMessage(`[]`)
	}
	meta := in.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_version (agent_id, version, name, model, system, description,
		    tools, mcp_servers, skills, metadata, created_at)
		 VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, string(in.Model), nullStr(in.System), nullStr(in.Description),
		string(tools), string(mcp), string(skills), string(meta), now,
	); err != nil {
		return nil, fmt.Errorf("insert agent_version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetAgent(ctx, id)
}

func (s *Store) GetAgent(ctx context.Context, id string) (*AgentRow, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT a.id, a.tenant_id, a.name, a.current_version, a.created_at, a.updated_at, a.archived_at,
		        v.model, v.system, v.description, v.tools, v.mcp_servers, v.skills, v.multiagent, v.metadata
		 FROM agent a
		 JOIN agent_version v ON v.agent_id = a.id AND v.version = a.current_version
		 WHERE a.id = ?`, id)
	r := &AgentRow{}
	var createdMs, updatedMs int64
	var model, tools, mcp, skills, multiagent, meta sql.NullString
	if err := row.Scan(
		&r.ID, &r.TenantID, &r.Name, &r.CurrentVersion, &createdMs, &updatedMs, &r.ArchivedAt,
		&model, &r.System, &r.Description, &tools, &mcp, &skills, &multiagent, &meta,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = time.UnixMilli(createdMs).UTC()
	r.UpdatedAt = time.UnixMilli(updatedMs).UTC()
	r.Version = r.CurrentVersion
	r.Model = json.RawMessage(model.String)
	r.Tools = json.RawMessage(tools.String)
	r.MCPServers = json.RawMessage(mcp.String)
	r.Skills = json.RawMessage(skills.String)
	if multiagent.Valid {
		r.Multiagent = json.RawMessage(multiagent.String)
	}
	r.Metadata = json.RawMessage(meta.String)
	return r, nil
}

func (s *Store) ListAgents(ctx context.Context, tenantID string, limit int) ([]*AgentRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM agent
		 WHERE tenant_id = ? AND archived_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		a, err := s.GetAgent(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAgent(ctx context.Context, id string, in UpdateAgentInput) (*AgentRow, error) {
	now := time.Now().UTC().UnixMilli()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var current int
	if err := tx.QueryRowContext(ctx, `SELECT current_version FROM agent WHERE id = ? AND archived_at IS NULL`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if in.Version != current {
		return nil, fmt.Errorf("version conflict: current version is %d", current)
	}
	next := current + 1
	tools := in.Tools
	if len(tools) == 0 {
		tools = json.RawMessage(`[]`)
	}
	mcp := in.MCPServers
	if len(mcp) == 0 {
		mcp = json.RawMessage(`[]`)
	}
	skills := in.Skills
	if len(skills) == 0 {
		skills = json.RawMessage(`[]`)
	}
	meta := in.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_version (agent_id, version, name, model, system, description,
		    tools, mcp_servers, skills, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, next, in.Name, string(in.Model), nullStr(in.System), nullStr(in.Description),
		string(tools), string(mcp), string(skills), string(meta), now,
	); err != nil {
		return nil, fmt.Errorf("insert agent_version: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent SET name = ?, current_version = ?, updated_at = ? WHERE id = ?`,
		in.Name, next, now, id,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAgent(ctx, id)
}

func (s *Store) ListAgentVersions(ctx context.Context, id string) ([]*AgentRow, error) {
	var tenantID string
	var archivedAt sql.NullInt64
	var agentCreated, agentUpdated int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT tenant_id, created_at, updated_at, archived_at FROM agent WHERE id = ?`, id,
	).Scan(&tenantID, &agentCreated, &agentUpdated, &archivedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT version, name, model, system, description, tools, mcp_servers, skills, multiagent, metadata, created_at
		 FROM agent_version WHERE agent_id = ? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentRow{}
	for rows.Next() {
		r := &AgentRow{ID: id, TenantID: tenantID, ArchivedAt: archivedAt}
		var createdMs int64
		var model, tools, mcp, skills, multiagent, meta sql.NullString
		if err := rows.Scan(&r.Version, &r.Name, &model, &r.System, &r.Description, &tools, &mcp, &skills, &multiagent, &meta, &createdMs); err != nil {
			return nil, err
		}
		r.CurrentVersion = r.Version
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		r.UpdatedAt = time.UnixMilli(agentUpdated).UTC()
		r.Model = json.RawMessage(model.String)
		r.Tools = json.RawMessage(tools.String)
		r.MCPServers = json.RawMessage(mcp.String)
		r.Skills = json.RawMessage(skills.String)
		if multiagent.Valid {
			r.Multiagent = json.RawMessage(multiagent.String)
		}
		r.Metadata = json.RawMessage(meta.String)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ArchiveAgent(ctx context.Context, id string) error {
	now := time.Now().UTC().UnixMilli()
	res, err := s.DB.ExecContext(ctx, `UPDATE agent SET archived_at = ?, updated_at = ? WHERE id = ? AND archived_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM agent WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
