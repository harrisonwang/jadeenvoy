package harness

import (
	"encoding/json"
	"strings"
)

const (
	permissionAlwaysAllow = "always_allow"
	permissionAlwaysAsk   = "always_ask"
)

type permissionDecider struct {
	agentDefault string
	agentTools   map[string]string
	mcpDefaults  map[string]string
	mcpTools     map[string]string
}

type permissionPolicyDecl struct {
	Type string `json:"type"`
}

type permissionToolConfig struct {
	Name             string               `json:"name"`
	PermissionPolicy permissionPolicyDecl `json:"permission_policy"`
}

type permissionDefaultConfig struct {
	PermissionPolicy permissionPolicyDecl `json:"permission_policy"`
}

type permissionToolsetEntry struct {
	Type          string                  `json:"type"`
	MCPServerName string                  `json:"mcp_server_name"`
	DefaultConfig permissionDefaultConfig `json:"default_config"`
	Configs       []permissionToolConfig  `json:"configs"`
}

func newPermissionDecider(toolsJSON json.RawMessage) permissionDecider {
	d := permissionDecider{
		agentDefault: permissionAlwaysAllow,
		agentTools:   map[string]string{},
		mcpDefaults:  map[string]string{},
		mcpTools:     map[string]string{},
	}
	var entries []permissionToolsetEntry
	if err := json.Unmarshal(toolsJSON, &entries); err != nil {
		return d
	}
	for _, e := range entries {
		switch e.Type {
		case "agent_toolset_20260401":
			if p := normalizePermission(e.DefaultConfig.PermissionPolicy.Type); p != "" {
				d.agentDefault = p
			}
			for _, c := range e.Configs {
				if c.Name == "" {
					continue
				}
				if p := normalizePermission(c.PermissionPolicy.Type); p != "" {
					d.agentTools[c.Name] = p
				}
			}
		case "mcp_toolset":
			server := e.MCPServerName
			p := normalizePermission(e.DefaultConfig.PermissionPolicy.Type)
			if p == "" {
				p = permissionAlwaysAsk
			}
			if server != "" {
				d.mcpDefaults[server] = p
			}
			for _, c := range e.Configs {
				if c.Name == "" {
					continue
				}
				if p := normalizePermission(c.PermissionPolicy.Type); p != "" {
					d.mcpTools[mcpPermissionKey(server, c.Name)] = p
				}
			}
		}
	}
	return d
}

func normalizePermission(p string) string {
	switch p {
	case permissionAlwaysAllow, permissionAlwaysAsk:
		return p
	default:
		return ""
	}
}

func (d permissionDecider) requiresApproval(toolName string, isMCP, isCustom bool) bool {
	if isCustom {
		return false
	}
	if isMCP {
		server, shortName := splitMCPToolName(toolName)
		policy := permissionAlwaysAsk
		if p := d.mcpDefaults[server]; p != "" {
			policy = p
		}
		if p := d.mcpTools[mcpPermissionKey(server, shortName)]; p != "" {
			policy = p
		}
		if p := d.mcpTools[mcpPermissionKey(server, toolName)]; p != "" {
			policy = p
		}
		return policy == permissionAlwaysAsk
	}
	policy := d.agentDefault
	if p := d.agentTools[toolName]; p != "" {
		policy = p
	}
	return policy == permissionAlwaysAsk
}

func mcpPermissionKey(server, toolName string) string {
	return server + "\x00" + toolName
}

func splitMCPToolName(name string) (server, toolName string) {
	if !strings.HasPrefix(name, mcpToolPrefix) {
		return "", name
	}
	rest := strings.TrimPrefix(name, mcpToolPrefix)
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 {
		return "", name
	}
	return parts[0], parts[1]
}
