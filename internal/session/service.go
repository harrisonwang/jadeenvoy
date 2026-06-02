// Package session 是 Session 状态机 + 生命周期。
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/obs"
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

// Create 用 agent 当前版本快照创建 session。
func (s *Service) Create(ctx context.Context, tenantID string, req apitypes.CreateSessionRequest) (*apitypes.Session, error) {
	if req.Agent.ID == "" {
		return nil, fmt.Errorf("agent is required")
	}
	// 拉 agent 当前版本快照
	a, err := s.st.GetAgent(ctx, req.Agent.ID)
	if err != nil {
		return nil, err
	}
	snap, _ := json.Marshal(agentSnapshot(a))

	// environment_id 校验（#6 / ADR）：显式传则必须存在；省略则回落到自动建的 default。
	envID := req.EnvironmentID
	if envID == "" {
		envID = "default"
		if err := s.st.EnsureDefaultEnvironment(ctx, tenantID); err != nil {
			return nil, fmt.Errorf("ensure default environment: %w", err)
		}
	} else {
		if _, err := s.st.GetEnvironment(ctx, envID); err != nil {
			if err == store.ErrNotFound {
				return nil, fmt.Errorf("environment not found: %s", envID)
			}
			return nil, err
		}
	}

	metaJSON, _ := json.Marshal(req.Metadata)
	if len(req.Metadata) == 0 {
		metaJSON = []byte(`{}`)
	}

	row, err := s.st.CreateSession(ctx, store.CreateSessionInput{
		TenantID:      tenantID,
		AgentID:       a.ID,
		AgentVersion:  a.Version,
		AgentSnapshot: snap,
		EnvironmentID: envID,
		Title:         req.Title,
		VaultIDs:      req.VaultIDs,
		Metadata:      metaJSON,
	})
	if err != nil {
		return nil, err
	}

	// 持久化 resources（V1 / M2 支持 memory_store）
	for _, res := range req.Resources {
		payload, _ := json.Marshal(res)
		if _, err := s.st.AddSessionResource(ctx, store.AddSessionResourceInput{
			SessionID: row.ID,
			Type:      res.Type,
			Payload:   payload,
		}); err != nil {
			return nil, fmt.Errorf("add resource %s: %w", res.Type, err)
		}
	}

	obs.SessionsCreated.Inc()
	return rowToAPI(row), nil
}

func (s *Service) Get(ctx context.Context, id string) (*apitypes.Session, error) {
	row, err := s.st.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return rowToAPI(row), nil
}

func (s *Service) List(ctx context.Context, tenantID string, limit int) ([]*apitypes.Session, error) {
	rows, err := s.st.ListSessions(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*apitypes.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToAPI(r))
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.st.DeleteSession(ctx, id)
}

func (s *Service) Update(ctx context.Context, id string, req apitypes.UpdateSessionRequest) (*apitypes.Session, error) {
	metaJSON, _ := json.Marshal(req.Metadata)
	if len(req.Metadata) == 0 {
		metaJSON = []byte(`{}`)
	}
	row, err := s.st.UpdateSession(ctx, id, req.Title, metaJSON, req.VaultIDs)
	if err != nil {
		return nil, err
	}
	return rowToAPI(row), nil
}

func (s *Service) Archive(ctx context.Context, id string) error {
	return s.st.ArchiveSession(ctx, id)
}

func rowToAPI(r *store.SessionRow) *apitypes.Session {
	out := &apitypes.Session{
		Type:          "session",
		ID:            r.ID,
		Status:        r.Status,
		Agent:         r.AgentSnapshot,
		EnvironmentID: r.EnvironmentID,
		VaultIDs:      []string{},
		Metadata:      map[string]string{},
		Usage: apitypes.SessionUsage{
			InputTokens:              r.UsageInput,
			OutputTokens:             r.UsageOutput,
			CacheCreationInputTokens: r.UsageCacheCreate,
			CacheReadInputTokens:     r.UsageCacheRead,
		},
		CreatedAt: r.CreatedAt,
	}
	if r.Title.Valid {
		out.Title = r.Title.String
	}
	t := r.UpdatedAt
	out.UpdatedAt = &t
	if r.ArchivedAt.Valid {
		at := time.UnixMilli(r.ArchivedAt.Int64).UTC()
		out.ArchivedAt = &at
	}
	if r.TerminatedAt.Valid {
		tt := time.UnixMilli(r.TerminatedAt.Int64).UTC()
		out.TerminatedAt = &tt
	}
	_ = json.Unmarshal(r.VaultIDs, &out.VaultIDs)
	_ = json.Unmarshal(r.Metadata, &out.Metadata)
	return out
}

// agentSnapshot 把 store.AgentRow 转成 session 里嵌入的 agent 配置（API 形状）。
func agentSnapshot(a *store.AgentRow) map[string]json.RawMessage {
	metadata := a.Metadata
	var meta map[string]string
	if err := json.Unmarshal(a.Metadata, &meta); err == nil {
		if raw, ok := meta[guardrailsMetadataKey]; ok {
			out := map[string]json.RawMessage{
				"type":        json.RawMessage(`"agent"`),
				"id":          jsonString(a.ID),
				"name":        jsonString(a.Name),
				"model":       a.Model,
				"tools":       a.Tools,
				"mcp_servers": a.MCPServers,
				"skills":      a.Skills,
				"guardrails":  json.RawMessage(raw),
				"version":     jsonInt(a.Version),
			}
			if len(a.Multiagent) > 0 && string(a.Multiagent) != "null" {
				out["multiagent"] = a.Multiagent
			}
			delete(meta, guardrailsMetadataKey)
			cleanMeta, _ := json.Marshal(meta)
			if len(cleanMeta) == 0 {
				cleanMeta = []byte(`{}`)
			}
			out["metadata"] = cleanMeta
			if a.System.Valid {
				out["system"] = jsonString(a.System.String)
			}
			if a.Description.Valid {
				out["description"] = jsonString(a.Description.String)
			}
			return out
		}
	}

	out := map[string]json.RawMessage{
		"type":        json.RawMessage(`"agent"`),
		"id":          jsonString(a.ID),
		"name":        jsonString(a.Name),
		"model":       a.Model,
		"tools":       a.Tools,
		"mcp_servers": a.MCPServers,
		"skills":      a.Skills,
		"metadata":    metadata,
		"version":     jsonInt(a.Version),
	}
	if len(a.Multiagent) > 0 && string(a.Multiagent) != "null" {
		out["multiagent"] = a.Multiagent
	}
	if a.System.Valid {
		out["system"] = jsonString(a.System.String)
	}
	if a.Description.Valid {
		out["description"] = jsonString(a.Description.String)
	}
	return out
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func jsonInt(i int) json.RawMessage {
	b, _ := json.Marshal(i)
	return b
}
