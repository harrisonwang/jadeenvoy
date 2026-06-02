package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/store"
)

type dashboardSnapshot struct {
	Type           string                 `json:"type"`
	Runtime        dashboardRuntime       `json:"runtime"`
	Counts         dashboardCounts        `json:"counts"`
	Sessions       dashboardSessions      `json:"sessions"`
	Usage          dashboardUsage         `json:"usage"`
	EventsByType   []dashboardCountByName `json:"events_by_type"`
	ToolActivity   dashboardToolActivity  `json:"tool_activity"`
	RecentSessions []dashboardSession     `json:"recent_sessions"`
	GeneratedAt    time.Time              `json:"generated_at"`
}

type dashboardRuntime struct {
	Status            string `json:"status"`
	Database          string `json:"database"`
	AuthMode          string `json:"auth_mode"`
	LLMProvider       string `json:"llm_provider"`
	DefaultAgentModel string `json:"default_agent_model"`
}

type dashboardCounts struct {
	Agents            int64 `json:"agents"`
	ArchivedAgents    int64 `json:"archived_agents"`
	Sessions          int64 `json:"sessions"`
	ArchivedSessions  int64 `json:"archived_sessions"`
	Environments      int64 `json:"environments"`
	Files             int64 `json:"files"`
	Skills            int64 `json:"skills"`
	MemoryStores      int64 `json:"memory_stores"`
	Memories          int64 `json:"memories"`
	MemoryVersions    int64 `json:"memory_versions"`
	Vaults            int64 `json:"vaults"`
	VaultCredentials  int64 `json:"vault_credentials"`
	APIKeys           int64 `json:"api_keys"`
	WebhookEndpoints  int64 `json:"webhook_endpoints"`
	WebhookDeliveries int64 `json:"webhook_deliveries"`
}

type dashboardSessions struct {
	Active   int64                  `json:"active"`
	ByStatus []dashboardCountByName `json:"by_status"`
}

type dashboardUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheCreateTokens int64 `json:"cache_create_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
}

type dashboardToolActivity struct {
	BuiltInToolUses int64 `json:"built_in_tool_uses"`
	BuiltInResults  int64 `json:"built_in_results"`
	MCPToolUses     int64 `json:"mcp_tool_uses"`
	MCPResults      int64 `json:"mcp_results"`
	CustomToolUses  int64 `json:"custom_tool_uses"`
	CustomResults   int64 `json:"custom_results"`
	ErroredResults  int64 `json:"errored_results"`
	RequiresAction  int64 `json:"requires_action"`
}

type dashboardSession struct {
	ID            string     `json:"id"`
	Title         string     `json:"title,omitempty"`
	AgentID       string     `json:"agent_id"`
	AgentVersion  int64      `json:"agent_version"`
	EnvironmentID string     `json:"environment_id"`
	Status        string     `json:"status"`
	EventCount    int64      `json:"event_count"`
	LastEventType string     `json:"last_event_type,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty"`
}

type dashboardCountByName struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func getDashboard(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := tenantFromCtx(r)
		snap, err := buildDashboardSnapshot(r.Context(), d, tenantID)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, snap)
	}
}

func buildDashboardSnapshot(ctx context.Context, d *Deps, tenantID string) (dashboardSnapshot, error) {
	st := d.Store
	counts, err := loadDashboardCounts(ctx, st, tenantID)
	if err != nil {
		return dashboardSnapshot{}, err
	}
	usage, err := loadDashboardUsage(ctx, st, tenantID)
	if err != nil {
		return dashboardSnapshot{}, err
	}
	sessionsByStatus, err := countByName(ctx, st, `
		SELECT status, COUNT(1)
		  FROM session
		 WHERE tenant_id = ? AND archived_at IS NULL
		 GROUP BY status
		 ORDER BY status`, tenantID)
	if err != nil {
		return dashboardSnapshot{}, err
	}
	eventsByType, err := countByName(ctx, st, `
		SELECT e.type, COUNT(1)
		  FROM session_event e
		  JOIN session s ON s.id = e.session_id
		 WHERE s.tenant_id = ?
		 GROUP BY e.type
		 ORDER BY COUNT(1) DESC, e.type
		 LIMIT 25`, tenantID)
	if err != nil {
		return dashboardSnapshot{}, err
	}
	toolActivity, err := loadDashboardToolActivity(ctx, st, tenantID)
	if err != nil {
		return dashboardSnapshot{}, err
	}
	recent, err := loadRecentDashboardSessions(ctx, st, tenantID)
	if err != nil {
		return dashboardSnapshot{}, err
	}

	return dashboardSnapshot{
		Type: "dashboard_snapshot",
		Runtime: dashboardRuntime{
			Status:            "ok",
			Database:          d.Store.Driver,
			AuthMode:          d.AuthMode,
			LLMProvider:       d.LLMProvider,
			DefaultAgentModel: d.DefaultAgentModel,
		},
		Counts: counts,
		Sessions: dashboardSessions{
			Active:   counts.Sessions,
			ByStatus: sessionsByStatus,
		},
		Usage:          usage,
		EventsByType:   eventsByType,
		ToolActivity:   toolActivity,
		RecentSessions: recent,
		GeneratedAt:    time.Now().UTC(),
	}, nil
}

func loadDashboardCounts(ctx context.Context, st *store.Store, tenantID string) (dashboardCounts, error) {
	var c dashboardCounts
	var err error
	if c.Agents, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM agent WHERE tenant_id = ? AND archived_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.ArchivedAgents, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM agent WHERE tenant_id = ? AND archived_at IS NOT NULL`, tenantID); err != nil {
		return c, err
	}
	if c.Sessions, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM session WHERE tenant_id = ? AND archived_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.ArchivedSessions, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM session WHERE tenant_id = ? AND archived_at IS NOT NULL`, tenantID); err != nil {
		return c, err
	}
	if c.Environments, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM environment WHERE tenant_id = ? AND archived_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.Files, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM file WHERE tenant_id = ?`, tenantID); err != nil {
		return c, err
	}
	if c.Skills, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM skill WHERE tenant_id = ?`, tenantID); err != nil {
		return c, err
	}
	if c.MemoryStores, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM memory_store WHERE tenant_id = ? AND archived_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.Memories, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM memory WHERE tenant_id = ?`, tenantID); err != nil {
		return c, err
	}
	if c.MemoryVersions, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM memory_version WHERE tenant_id = ?`, tenantID); err != nil {
		return c, err
	}
	if c.Vaults, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM vault WHERE tenant_id = ? AND archived_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.VaultCredentials, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM vault_credential WHERE tenant_id = ? AND archived_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.APIKeys, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM api_key WHERE tenant_id = ? AND revoked_at IS NULL`, tenantID); err != nil {
		return c, err
	}
	if c.WebhookEndpoints, err = scalarCount(ctx, st, `SELECT COUNT(1) FROM webhook_endpoint WHERE tenant_id = ?`, tenantID); err != nil {
		return c, err
	}
	if c.WebhookDeliveries, err = scalarCount(ctx, st, `
		SELECT COUNT(1)
		  FROM webhook_delivery d
		  JOIN webhook_endpoint e ON e.id = d.endpoint_id
		 WHERE e.tenant_id = ?`, tenantID); err != nil {
		return c, err
	}
	return c, nil
}

func loadDashboardUsage(ctx context.Context, st *store.Store, tenantID string) (dashboardUsage, error) {
	var u dashboardUsage
	err := st.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(usage_input_tokens), 0),
		       COALESCE(SUM(usage_output_tokens), 0),
		       COALESCE(SUM(usage_cache_create_tokens), 0),
		       COALESCE(SUM(usage_cache_read_tokens), 0)
		  FROM session
		 WHERE tenant_id = ?`, tenantID).Scan(&u.InputTokens, &u.OutputTokens, &u.CacheCreateTokens, &u.CacheReadTokens)
	if err != nil {
		return u, err
	}
	u.TotalTokens = u.InputTokens + u.OutputTokens + u.CacheCreateTokens + u.CacheReadTokens
	return u, nil
}

func loadDashboardToolActivity(ctx context.Context, st *store.Store, tenantID string) (dashboardToolActivity, error) {
	var a dashboardToolActivity
	var err error
	if a.BuiltInToolUses, err = countEvents(ctx, st, tenantID, "agent.tool_use"); err != nil {
		return a, err
	}
	if a.BuiltInResults, err = countEvents(ctx, st, tenantID, "agent.tool_result"); err != nil {
		return a, err
	}
	if a.MCPToolUses, err = countEvents(ctx, st, tenantID, "agent.mcp_tool_use"); err != nil {
		return a, err
	}
	if a.MCPResults, err = countEvents(ctx, st, tenantID, "agent.mcp_tool_result"); err != nil {
		return a, err
	}
	if a.CustomToolUses, err = countEvents(ctx, st, tenantID, "agent.custom_tool_use"); err != nil {
		return a, err
	}
	if a.CustomResults, err = countEvents(ctx, st, tenantID, "user.custom_tool_result"); err != nil {
		return a, err
	}
	legacyRequiresAction, err := countEvents(ctx, st, tenantID, "session.status_requires_action")
	if err != nil {
		return a, err
	}
	idleRequiresAction, err := scalarCount(ctx, st, `
		SELECT COUNT(1)
		  FROM session_event e
		  JOIN session s ON s.id = e.session_id
		 WHERE s.tenant_id = ?
		   AND e.type = 'session.status_idle'
		   AND `+st.JSONTextEquals("e.payload", []string{"stop_reason", "type"}, "requires_action"), tenantID)
	if err != nil {
		return a, err
	}
	a.RequiresAction = legacyRequiresAction + idleRequiresAction
	if a.ErroredResults, err = scalarCount(ctx, st, `
		SELECT COUNT(1)
		  FROM session_event e
		  JOIN session s ON s.id = e.session_id
		 WHERE s.tenant_id = ?
		   AND e.type IN ('agent.tool_result', 'agent.mcp_tool_result', 'user.custom_tool_result')
		   AND `+st.JSONBoolTrue("e.payload", "is_error"), tenantID); err != nil {
		return a, err
	}
	return a, nil
}

func loadRecentDashboardSessions(ctx context.Context, st *store.Store, tenantID string) ([]dashboardSession, error) {
	rows, err := st.QueryContext(ctx, `
		SELECT s.id, COALESCE(s.title, ''), s.agent_id, s.agent_version, s.environment_id,
		       s.status, s.created_at, s.updated_at, s.archived_at,
		       COUNT(e.id) AS event_count,
		       COALESCE((
		         SELECT e2.type
		           FROM session_event e2
		          WHERE e2.session_id = s.id
		          ORDER BY e2.seq DESC
		          LIMIT 1
		       ), '') AS last_event_type
		  FROM session s
		  LEFT JOIN session_event e ON e.session_id = s.id
		 WHERE s.tenant_id = ?
		 GROUP BY s.id
		 ORDER BY s.created_at DESC
		 LIMIT 10`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []dashboardSession{}
	for rows.Next() {
		var item dashboardSession
		var title string
		var createdMs, updatedMs int64
		var archived sql.NullInt64
		if err := rows.Scan(
			&item.ID, &title, &item.AgentID, &item.AgentVersion, &item.EnvironmentID,
			&item.Status, &createdMs, &updatedMs, &archived, &item.EventCount, &item.LastEventType,
		); err != nil {
			return nil, err
		}
		item.Title = title
		item.CreatedAt = time.UnixMilli(createdMs).UTC()
		item.UpdatedAt = time.UnixMilli(updatedMs).UTC()
		if archived.Valid {
			t := time.UnixMilli(archived.Int64).UTC()
			item.ArchivedAt = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scalarCount(ctx context.Context, st *store.Store, query string, args ...any) (int64, error) {
	var count int64
	if err := st.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func countEvents(ctx context.Context, st *store.Store, tenantID, eventType string) (int64, error) {
	return scalarCount(ctx, st, `
		SELECT COUNT(1)
		  FROM session_event e
		  JOIN session s ON s.id = e.session_id
		 WHERE s.tenant_id = ? AND e.type = ?`, tenantID, eventType)
}

func countByName(ctx context.Context, st *store.Store, query string, args ...any) ([]dashboardCountByName, error) {
	rows, err := st.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []dashboardCountByName{}
	for rows.Next() {
		var item dashboardCountByName
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
