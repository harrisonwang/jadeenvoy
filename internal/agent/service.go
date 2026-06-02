// Package agent 是 Agent 实体的业务层。
package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

type Service struct {
	st *store.Store
}

const guardrailsMetadataKey = "_jadeenvoy_guardrails"

func New(st *store.Store) *Service {
	return &Service{st: st}
}

// Create 创建 agent。
func (s *Service) Create(ctx context.Context, tenantID string, req apitypes.CreateAgentRequest) (*apitypes.Agent, error) {
	model, _ := json.Marshal(req.Model)
	tools, _ := json.Marshal(req.Tools)
	skills, _ := json.Marshal(req.Skills)
	if len(skills) == 0 || string(skills) == "null" {
		skills = []byte(`[]`)
	}
	mcpServers, _ := json.Marshal(req.MCPServers)
	if len(mcpServers) == 0 || string(mcpServers) == "null" {
		mcpServers = []byte(`[]`)
	}
	meta, err := encodeAgentMetadata(req.Metadata, req.Guardrails)
	if err != nil {
		return nil, err
	}

	row, err := s.st.CreateAgent(ctx, store.CreateAgentInput{
		TenantID:    tenantID,
		Name:        req.Name,
		Model:       model,
		System:      req.System,
		Description: req.Description,
		Tools:       tools,
		MCPServers:  mcpServers,
		Skills:      skills,
		Multiagent:  req.Multiagent,
		Metadata:    meta,
	})
	if err != nil {
		return nil, err
	}
	return rowToAPI(row), nil
}

func (s *Service) Get(ctx context.Context, id string) (*apitypes.Agent, error) {
	row, err := s.st.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	return rowToAPI(row), nil
}

func (s *Service) List(ctx context.Context, tenantID string, limit int) ([]*apitypes.Agent, error) {
	rows, err := s.st.ListAgents(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*apitypes.Agent, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToAPI(r))
	}
	return out, nil
}

func (s *Service) Update(ctx context.Context, id string, req apitypes.UpdateAgentRequest) (*apitypes.Agent, error) {
	model, _ := json.Marshal(req.Model)
	tools, _ := json.Marshal(req.Tools)
	skills, _ := json.Marshal(req.Skills)
	if len(skills) == 0 || string(skills) == "null" {
		skills = []byte(`[]`)
	}
	mcpServers, _ := json.Marshal(req.MCPServers)
	if len(mcpServers) == 0 || string(mcpServers) == "null" {
		mcpServers = []byte(`[]`)
	}
	meta, err := encodeAgentMetadata(req.Metadata, req.Guardrails)
	if err != nil {
		return nil, err
	}
	row, err := s.st.UpdateAgent(ctx, id, store.UpdateAgentInput{
		Name:        req.Name,
		Model:       model,
		System:      req.System,
		Description: req.Description,
		Tools:       tools,
		MCPServers:  mcpServers,
		Skills:      skills,
		Multiagent:  req.Multiagent,
		Metadata:    meta,
		Version:     req.Version,
	})
	if err != nil {
		return nil, err
	}
	return rowToAPI(row), nil
}

func (s *Service) Versions(ctx context.Context, id string) ([]*apitypes.Agent, error) {
	rows, err := s.st.ListAgentVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]*apitypes.Agent, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToAPI(r))
	}
	return out, nil
}

func (s *Service) Archive(ctx context.Context, id string) error {
	return s.st.ArchiveAgent(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.st.DeleteAgent(ctx, id)
}

func rowToAPI(r *store.AgentRow) *apitypes.Agent {
	a := &apitypes.Agent{
		Type:       "agent",
		ID:         r.ID,
		Name:       r.Name,
		Version:    r.Version,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
		Tools:      []apitypes.ToolEntry{},
		Skills:     []json.RawMessage{},
		MCPServers: []json.RawMessage{},
		Metadata:   map[string]string{},
	}
	if r.System.Valid {
		a.System = r.System.String
	}
	if r.Description.Valid {
		a.Description = r.Description.String
	}
	if r.ArchivedAt.Valid {
		t := time.UnixMilli(r.ArchivedAt.Int64).UTC()
		a.ArchivedAt = &t
	}
	_ = json.Unmarshal(r.Model, &a.Model)
	_ = json.Unmarshal(r.Tools, &a.Tools)
	_ = json.Unmarshal(r.Metadata, &a.Metadata)
	if raw, ok := a.Metadata[guardrailsMetadataKey]; ok {
		var guardrails apitypes.AgentGuardrails
		if err := json.Unmarshal([]byte(raw), &guardrails); err == nil {
			a.Guardrails = &guardrails
		}
		delete(a.Metadata, guardrailsMetadataKey)
	}
	// Skills / MCPServers 保持 raw
	if len(r.Skills) > 0 && string(r.Skills) != "null" {
		var arr []json.RawMessage
		if err := json.Unmarshal(r.Skills, &arr); err == nil {
			a.Skills = arr
		}
	}
	if len(r.MCPServers) > 0 && string(r.MCPServers) != "null" {
		var arr []json.RawMessage
		if err := json.Unmarshal(r.MCPServers, &arr); err == nil {
			a.MCPServers = arr
		}
	}
	if len(r.Multiagent) > 0 && string(r.Multiagent) != "null" {
		a.Multiagent = r.Multiagent
	}
	return a
}

func encodeAgentMetadata(meta map[string]string, guardrails *apitypes.AgentGuardrails) ([]byte, error) {
	out := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		if k == guardrailsMetadataKey {
			continue
		}
		out[k] = v
	}
	if guardrails != nil {
		raw, err := json.Marshal(guardrails)
		if err != nil {
			return nil, err
		}
		out[guardrailsMetadataKey] = string(raw)
	}
	if len(out) == 0 {
		return []byte(`{}`), nil
	}
	return json.Marshal(out)
}
