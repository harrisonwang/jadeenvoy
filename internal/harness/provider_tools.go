package harness

import (
	"encoding/json"

	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/tool"
)

// toProviderTools 转换 tool registry → provider 形状。
func toProviderTools(defs []tool.ToolDef) []provider.ToolDef {
	out := make([]provider.ToolDef, len(defs))
	for i, d := range defs {
		out[i] = provider.ToolDef{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.InputSchema,
		}
	}
	return out
}

// parseCustomToolDefs 从 agent snapshot 的 tools JSON 中提取 type=custom 的工具定义。
func parseCustomToolDefs(toolsJSON json.RawMessage) []tool.ToolDef {
	type entry struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	var entries []entry
	_ = json.Unmarshal(toolsJSON, &entries)
	var defs []tool.ToolDef
	for _, e := range entries {
		if e.Type != "custom" || e.Name == "" {
			continue
		}
		defs = append(defs, tool.ToolDef{
			Name:        e.Name,
			Description: e.Description,
			InputSchema: e.InputSchema,
		})
	}
	return defs
}
