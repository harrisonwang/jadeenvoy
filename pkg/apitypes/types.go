// Package apitypes 是 JadeEnvoy REST API 的请求/响应 shape。
// 跟 Anthropic Managed Agents spec 字段名对齐。SDK 用户也会引用本包。
package apitypes

import (
	"encoding/json"
	"time"
)

// ─── Agent ────────────────────────────────────────────────────────────────

type AgentModel struct {
	ID    string `json:"id"`
	Speed string `json:"speed,omitempty"`
}

// UnmarshalJSON 允许 "claude-..." 字符串 或 {id, speed} 对象。
func (m *AgentModel) UnmarshalJSON(data []byte) error {
	// 先试字符串
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.ID = s
		return nil
	}
	// 再试对象
	type raw AgentModel
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*m = AgentModel(r)
	return nil
}

type ToolEntry struct {
	Type          string          `json:"type"`
	Name          string          `json:"name,omitempty"` // for custom
	MCPServerName string          `json:"mcp_server_name,omitempty"`
	Description   string          `json:"description,omitempty"`  // for custom
	InputSchema   json.RawMessage `json:"input_schema,omitempty"` // for custom
	Configs       json.RawMessage `json:"configs,omitempty"`      // toolset 子工具配置
	DefaultConfig json.RawMessage `json:"default_config,omitempty"`
}

type AgentGuardrails struct {
	ToolPermissions *ToolPermissionPolicy `json:"tool_permissions,omitempty"`
}

type ToolPermissionPolicy struct {
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`
}

type Agent struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Model       AgentModel        `json:"model"`
	System      string            `json:"system,omitempty"`
	Description string            `json:"description,omitempty"`
	Tools       []ToolEntry       `json:"tools"`
	Skills      []json.RawMessage `json:"skills"`
	MCPServers  []json.RawMessage `json:"mcp_servers"`
	Multiagent  json.RawMessage   `json:"multiagent,omitempty"`
	Guardrails  *AgentGuardrails  `json:"guardrails,omitempty"`
	Metadata    map[string]string `json:"metadata"`
	Version     int               `json:"version"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	ArchivedAt  *time.Time        `json:"archived_at"`
}

type CreateAgentRequest struct {
	Name        string            `json:"name"`
	Model       AgentModel        `json:"model"`
	System      string            `json:"system,omitempty"`
	Description string            `json:"description,omitempty"`
	Tools       []ToolEntry       `json:"tools"`
	MCPServers  []json.RawMessage `json:"mcp_servers,omitempty"`
	Skills      []json.RawMessage `json:"skills,omitempty"`
	Multiagent  json.RawMessage   `json:"multiagent,omitempty"`
	Guardrails  *AgentGuardrails  `json:"guardrails,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type UpdateAgentRequest struct {
	Name        string            `json:"name"`
	Model       AgentModel        `json:"model"`
	System      string            `json:"system,omitempty"`
	Description string            `json:"description,omitempty"`
	Tools       []ToolEntry       `json:"tools"`
	MCPServers  []json.RawMessage `json:"mcp_servers,omitempty"`
	Skills      []json.RawMessage `json:"skills,omitempty"`
	Multiagent  json.RawMessage   `json:"multiagent,omitempty"`
	Guardrails  *AgentGuardrails  `json:"guardrails,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Version     int               `json:"version"`
}

// ─── Session ──────────────────────────────────────────────────────────────

type SessionRef struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
}

// UnmarshalJSON 允许字符串（最新版本）或 {type, id, version} 对象。
func (s *SessionRef) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		s.Type = "agent"
		s.ID = str
		return nil
	}
	type raw SessionRef
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*s = SessionRef(r)
	return nil
}

type Session struct {
	Type          string            `json:"type"`
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	Agent         json.RawMessage   `json:"agent"` // 完整 agent snapshot
	EnvironmentID string            `json:"environment_id"`
	VaultIDs      []string          `json:"vault_ids"`
	Title         string            `json:"title,omitempty"`
	Metadata      map[string]string `json:"metadata"`
	Usage         SessionUsage      `json:"usage"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     *time.Time        `json:"updated_at"`
	ArchivedAt    *time.Time        `json:"archived_at"`
	TerminatedAt  *time.Time        `json:"terminated_at"`
}

type SessionUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type CreateSessionRequest struct {
	Agent         SessionRef        `json:"agent"`
	EnvironmentID string            `json:"environment_id,omitempty"`
	VaultIDs      []string          `json:"vault_ids,omitempty"`
	Resources     []ResourceEntry   `json:"resources,omitempty"`
	Title         string            `json:"title,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type UpdateSessionRequest struct {
	Title    string            `json:"title,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	VaultIDs []string          `json:"vault_ids,omitempty"`
}

// ResourceEntry 是 session 挂载的资源（M2 支持 memory_store / file；M3 加 github_repository）。
type ResourceEntry struct {
	Type          string `json:"type"` // memory_store / file / github_repository
	MemoryStoreID string `json:"memory_store_id,omitempty"`
	Access        string `json:"access,omitempty"` // read_only / read_write
	Instructions  string `json:"instructions,omitempty"`
	FileID        string `json:"file_id,omitempty"`
	MountPath     string `json:"mount_path,omitempty"`
}

// ─── Environments ────────────────────────────────────────────────────────────

type Environment struct {
	Type       string          `json:"type"` // "environment"
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Config     json.RawMessage `json:"config"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  *time.Time      `json:"updated_at"`
	ArchivedAt *time.Time      `json:"archived_at"`
}

type CreateEnvironmentRequest struct {
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

// ─── Events ────────────────────────────────────────────────────────────────

type Event struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id,omitempty"`
	ThreadID    string          `json:"session_thread_id,omitempty"`
	Seq         int64           `json:"seq,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"`
	Payload     json.RawMessage `json:"-"` // 内部用，序列化时合并到顶层
	ProcessedAt *int64          `json:"processed_at,omitempty"`
	TS          int64           `json:"ts,omitempty"`
	CreatedAt   *time.Time      `json:"created_at,omitempty"`
}

type SendEventsRequest struct {
	Events []json.RawMessage `json:"events"`
}

type EventListResponse struct {
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
}

// ─── Files (M2) ────────────────────────────────────────────────────────────

type File struct {
	Type        string     `json:"type"`
	ID          string     `json:"id"`
	Filename    string     `json:"filename"`
	ContentType string     `json:"content_type"`
	Size        int64      `json:"size"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

// ─── Skills (M2) ───────────────────────────────────────────────────────────

type Skill struct {
	Type        string      `json:"type"`
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Files       []SkillFile `json:"files,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   *time.Time  `json:"updated_at"`
}

type SkillFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"` // only in responses
}

type CreateSkillRequest struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Files       []SkillFile `json:"files,omitempty"`
}

// ─── Vault (M1/ADR-0015) ───────────────────────────────────────────────────

type Vault struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   *time.Time        `json:"updated_at"`
	ArchivedAt  *time.Time        `json:"archived_at"`
}

type CreateVaultRequest struct {
	DisplayName string            `json:"display_name"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Credential 是 vault 凭据的响应形状。secret（token）永不返回。
type Credential struct {
	Type        string             `json:"type"`
	ID          string             `json:"id"`
	VaultID     string             `json:"vault_id"`
	DisplayName string             `json:"display_name"`
	Auth        CredentialAuthView `json:"auth"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   *time.Time         `json:"updated_at"`
	ArchivedAt  *time.Time         `json:"archived_at"`
}

type CredentialAuthView struct {
	Type         string `json:"type"`
	MCPServerURL string `json:"mcp_server_url"`
	// token 永不出现在响应里
}

type CreateCredentialRequest struct {
	DisplayName string              `json:"display_name"`
	Auth        CredentialAuthInput `json:"auth"`
}

type CredentialAuthInput struct {
	Type         string                       `json:"type"` // static_bearer / mcp_oauth
	MCPServerURL string                       `json:"mcp_server_url"`
	Token        string                       `json:"token"`
	AccessToken  string                       `json:"access_token"`
	ExpiresAt    string                       `json:"expires_at"`
	Refresh      *CredentialOAuthRefreshInput `json:"refresh,omitempty"`
}

type CredentialOAuthRefreshInput struct {
	TokenEndpoint     string                        `json:"token_endpoint"`
	ClientID          string                        `json:"client_id"`
	Scope             string                        `json:"scope,omitempty"`
	RefreshToken      string                        `json:"refresh_token"`
	TokenEndpointAuth CredentialOAuthEndpointAuthIn `json:"token_endpoint_auth"`
}

type CredentialOAuthEndpointAuthIn struct {
	Type         string `json:"type"` // none / client_secret_basic / client_secret_post
	ClientSecret string `json:"client_secret,omitempty"`
}

// ─── List wrappers ────────────────────────────────────────────────────────

type ListResponse[T any] struct {
	Data    []T  `json:"data"`
	HasMore bool `json:"has_more"`
}

// ─── Error ────────────────────────────────────────────────────────────────

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}
