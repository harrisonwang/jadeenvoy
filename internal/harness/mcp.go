package harness

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/harrisonwang/jadeenvoy/internal/mcp"
	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/tool"
)

// hostOf 取 URL 的 host（用于按 host 匹配 vault 凭据）。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// mcpToolPrefix 把 server 名与工具名编进 LLM 可见的工具名：mcp__<server>__<tool>（对齐 spec）。
const mcpToolPrefix = "mcp__"

// mcpSession 持有一个 turn 内连上的所有 MCP server + 工具名→client 路由。
type mcpSession struct {
	clients map[string]*mcp.Client // serverName → client
	defs    []tool.ToolDef         // 暴露给 LLM 的工具定义（已带 mcp__ 前缀）
	route   map[string]mcpRoute    // 前缀工具名 → 路由
}

type mcpRoute struct {
	server   string
	toolName string // server 上的原始工具名
}

// mcpServerDecl 是 agent_snapshot.mcp_servers 里的一条声明。
type mcpServerDecl struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// credResolver 按 host 返回要注入 MCP 请求的 Authorization 头值（空表示无鉴权）。
// 由 harness 用 vault.Resolve 包成闭包传入，避免 mcp.go 直接依赖 vault（ADR-0026）。
type credResolver func(host string) string

// connectMCP 解析 agent_snapshot 的 mcp_servers，连接 + 发现工具。
// 单个 server 失败仅告警跳过（degraded），不让一个挂掉的 server 拖死整个 turn（ADR-0024）。
// resolveAuth 非空时按 server host 注入 Authorization（ADR-0026）。
func connectMCP(ctx context.Context, agentSnapshot json.RawMessage, resolveAuth credResolver) *mcpSession {
	var snap struct {
		MCPServers json.RawMessage `json:"mcp_servers"`
	}
	_ = json.Unmarshal(agentSnapshot, &snap)
	if len(snap.MCPServers) == 0 {
		return nil
	}
	var decls []mcpServerDecl
	if err := json.Unmarshal(snap.MCPServers, &decls); err != nil {
		return nil
	}

	ms := &mcpSession{
		clients: map[string]*mcp.Client{},
		route:   map[string]mcpRoute{},
	}
	log := obs.Logger()
	for _, d := range decls {
		if d.Type != "url" || d.Name == "" || d.URL == "" {
			if d.Type != "" && d.Type != "url" {
				log.Warn("harness.mcp.unsupported_type", "type", d.Type, "name", d.Name)
			}
			continue
		}
		c := mcp.NewClient(d.URL)
		// 按 host 注入 vault static_bearer（ADR-0026）。
		if resolveAuth != nil {
			if host := hostOf(d.URL); host != "" {
				if tok := resolveAuth(host); tok != "" {
					c.SetAuthorization("Bearer " + tok)
				}
			}
		}
		if err := c.Initialize(ctx); err != nil {
			log.Warn("harness.mcp.init_failed", "server", d.Name, "err", err.Error())
			continue
		}
		tools, err := c.ListTools(ctx)
		if err != nil {
			log.Warn("harness.mcp.list_failed", "server", d.Name, "err", err.Error())
			continue
		}
		ms.clients[d.Name] = c
		for _, mt := range tools {
			fqName := mcpToolPrefix + d.Name + "__" + mt.Name
			ms.defs = append(ms.defs, tool.ToolDef{
				Name:        fqName,
				Description: mt.Description,
				InputSchema: mt.InputSchema,
			})
			ms.route[fqName] = mcpRoute{server: d.Name, toolName: mt.Name}
		}
		log.Info("harness.mcp.connected", "server", d.Name, "tools", len(tools))
	}
	if len(ms.defs) == 0 {
		return nil
	}
	return ms
}

// isMCPTool 报告某工具名是否路由到 MCP。
func (m *mcpSession) isMCPTool(name string) bool {
	if m == nil {
		return false
	}
	_, ok := m.route[name]
	return ok
}

// toolDefs 返回要 append 给 LLM 的 MCP 工具定义。
func (m *mcpSession) toolDefs() []tool.ToolDef {
	if m == nil {
		return nil
	}
	return m.defs
}

// call 路由一次 MCP 工具调用到对应 server。
func (m *mcpSession) call(ctx context.Context, fqName string, input json.RawMessage) (content string, isError bool) {
	r, ok := m.route[fqName]
	if !ok {
		return "unknown mcp tool: " + fqName, true
	}
	c := m.clients[r.server]
	if c == nil {
		return "mcp server not connected: " + r.server, true
	}
	res, err := c.CallTool(ctx, r.toolName, input)
	if err != nil {
		return "mcp call failed: " + err.Error(), true
	}
	return res.Text(), res.IsError
}

// providerToolDefs 把 MCP 工具定义转成 provider.ToolDef。
func (m *mcpSession) providerToolDefs() []provider.ToolDef {
	if m == nil {
		return nil
	}
	out := make([]provider.ToolDef, 0, len(m.defs))
	for _, d := range m.defs {
		schema := d.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, provider.ToolDef{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: schema,
		})
	}
	return out
}
