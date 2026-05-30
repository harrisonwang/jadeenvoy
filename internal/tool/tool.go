// Package tool 是内置工具实现。
package tool

import (
	"context"
	"encoding/json"

	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
)

// Tool 是 LLM 看到的工具单元。
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Execute(ctx context.Context, sb sandbox.Sandbox, input json.RawMessage) (Result, error)
}

// Result 是工具执行结果。
type Result struct {
	Content string // 给 LLM 看的文本（合并 stdout + stderr）
	IsError bool
}

// Registry 是内置工具名 → 实现的 lookup。自定义工具定义是 per-turn 状态，
// 不应写入全局 Registry，否则并发 session 会互相污染。
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// BuiltinDefs 返回所有内置工具的 LLM 定义。
func (r *Registry) BuiltinDefs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return defs
}

// Defs 保持向后兼容，返回内置工具定义。自定义工具请由调用方按 turn 附加。
func (r *Registry) Defs() []ToolDef {
	return r.BuiltinDefs()
}

// ToolDef 是给 LLM provider 的工具定义（跟 provider.ToolDef 同形状但解耦循环依赖）。
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
