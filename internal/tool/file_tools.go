package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
)

// ─── read ─────────────────────────────────────────────────────────────────

type ReadTool struct{}

func (ReadTool) Name() string { return "read" }
func (ReadTool) Description() string {
	return "Read a file from the sandbox filesystem. Returns the file content. " +
		"Optional offset and limit for partial reads of large files."
}
func (ReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":   {"type": "string", "description": "Absolute path or relative to workdir"},
			"offset": {"type": "integer", "description": "Line offset (1-based)"},
			"limit":  {"type": "integer", "description": "Max lines to read"}
		},
		"required": ["path"]
	}`)
}

type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (ReadTool) Execute(ctx context.Context, sb sandbox.Sandbox, raw json.RawMessage) (Result, error) {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if in.Path == "" {
		return Result{Content: "path is required", IsError: true}, nil
	}
	// 通过 bash 实现，简单可控
	cmd := fmt.Sprintf("cat %s", shellQuote(in.Path))
	if in.Offset > 0 || in.Limit > 0 {
		off := in.Offset
		if off <= 0 {
			off = 1
		}
		if in.Limit > 0 {
			cmd = fmt.Sprintf("sed -n '%d,%dp' %s", off, off+in.Limit-1, shellQuote(in.Path))
		} else {
			cmd = fmt.Sprintf("tail -n +%d %s", off, shellQuote(in.Path))
		}
	}
	res, err := sb.Exec(ctx, sandbox.Command{Args: []string{"bash", "-c", cmd}})
	if err != nil {
		return Result{Content: "exec error: " + err.Error(), IsError: true}, nil
	}
	if res.ExitCode != 0 {
		return Result{
			Content: fmt.Sprintf("read failed (exit %d):\n%s", res.ExitCode, res.Stderr),
			IsError: true,
		}, nil
	}
	return Result{Content: res.Stdout}, nil
}

// ─── write ────────────────────────────────────────────────────────────────

type WriteTool struct{}

func (WriteTool) Name() string { return "write" }
func (WriteTool) Description() string {
	return "Write (or overwrite) a file in the sandbox. Creates parent dirs."
}
func (WriteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":    {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (WriteTool) Execute(ctx context.Context, sb sandbox.Sandbox, raw json.RawMessage) (Result, error) {
	var in writeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if in.Path == "" {
		return Result{Content: "path is required", IsError: true}, nil
	}
	// mkdir -p + 写文件（用 base64 避免 shell quoting 噩梦）
	encoded := base64Std.EncodeToString([]byte(in.Content))
	cmd := fmt.Sprintf("mkdir -p %s && echo %s | base64 -d > %s",
		shellQuote(dirOf(in.Path)),
		shellQuote(encoded),
		shellQuote(in.Path),
	)
	res, err := sb.Exec(ctx, sandbox.Command{Args: []string{"bash", "-c", cmd}})
	if err != nil {
		return Result{Content: "exec error: " + err.Error(), IsError: true}, nil
	}
	if res.ExitCode != 0 {
		return Result{
			Content: fmt.Sprintf("write failed (exit %d):\n%s", res.ExitCode, res.Stderr),
			IsError: true,
		}, nil
	}
	return Result{
		Content: fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path),
	}, nil
}

// ─── edit ─────────────────────────────────────────────────────────────────

type EditTool struct{}

func (EditTool) Name() string { return "edit" }
func (EditTool) Description() string {
	return "Perform an exact string replacement in a file. old_string must be unique in the file."
}
func (EditTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":       {"type": "string"},
			"old_string": {"type": "string"},
			"new_string": {"type": "string"}
		},
		"required": ["path", "old_string", "new_string"]
	}`)
}

type editInput struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func (EditTool) Execute(ctx context.Context, sb sandbox.Sandbox, raw json.RawMessage) (Result, error) {
	var in editInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if in.Path == "" || in.OldString == "" {
		return Result{Content: "path and old_string are required", IsError: true}, nil
	}
	// 用 base64 + Python（更可控，无需正则转义）
	pyScript := fmt.Sprintf(`
import sys, base64
with open(%q, 'r', encoding='utf-8') as f:
    s = f.read()
old = base64.b64decode(%q).decode('utf-8')
new = base64.b64decode(%q).decode('utf-8')
count = s.count(old)
if count == 0:
    print("ERROR: old_string not found", file=sys.stderr)
    sys.exit(2)
if count > 1:
    print(f"ERROR: old_string appears {count} times, must be unique", file=sys.stderr)
    sys.exit(3)
s = s.replace(old, new)
with open(%q, 'w', encoding='utf-8') as f:
    f.write(s)
print(f"edited {%q}: {count} replacement(s)")
`,
		in.Path,
		base64Std.EncodeToString([]byte(in.OldString)),
		base64Std.EncodeToString([]byte(in.NewString)),
		in.Path,
		in.Path,
	)
	res, err := sb.Exec(ctx, sandbox.Command{Args: []string{"python3", "-c", pyScript}})
	if err != nil {
		return Result{Content: "exec error: " + err.Error(), IsError: true}, nil
	}
	if res.ExitCode != 0 {
		return Result{
			Content: fmt.Sprintf("edit failed (exit %d):\n%s", res.ExitCode, res.Stderr),
			IsError: true,
		}, nil
	}
	return Result{Content: strings.TrimSpace(res.Stdout)}, nil
}

// ─── glob ─────────────────────────────────────────────────────────────────

type GlobTool struct{}

func (GlobTool) Name() string { return "glob" }
func (GlobTool) Description() string {
	return "Find files matching a glob pattern. Supports ** for recursive matching."
}
func (GlobTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Glob pattern, e.g. **/*.go"}
		},
		"required": ["pattern"]
	}`)
}

type globInput struct {
	Pattern string `json:"pattern"`
}

func (GlobTool) Execute(ctx context.Context, sb sandbox.Sandbox, raw json.RawMessage) (Result, error) {
	var in globInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if in.Pattern == "" {
		return Result{Content: "pattern is required", IsError: true}, nil
	}
	// 用 find（不依赖 globstar shell options）
	cmd := fmt.Sprintf("find . -name %s -type f 2>/dev/null | head -100", shellQuote(in.Pattern))
	if strings.Contains(in.Pattern, "/") {
		// 路径分隔符存在时用 -path 替代 -name
		cmd = fmt.Sprintf("find . -path %s -type f 2>/dev/null | head -100", shellQuote(in.Pattern))
	}
	res, err := sb.Exec(ctx, sandbox.Command{Args: []string{"bash", "-c", cmd}})
	if err != nil {
		return Result{Content: "exec error: " + err.Error(), IsError: true}, nil
	}
	if res.Stdout == "" {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: res.Stdout}, nil
}

// ─── grep ─────────────────────────────────────────────────────────────────

type GrepTool struct{}

func (GrepTool) Name() string { return "grep" }
func (GrepTool) Description() string {
	return "Search file contents using a regex pattern. Returns matching lines with file:line prefix."
}
func (GrepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string"},
			"path":    {"type": "string", "description": "Directory or file to search; default ."},
			"glob":    {"type": "string", "description": "Filename pattern filter, e.g. *.go"}
		},
		"required": ["pattern"]
	}`)
}

type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
}

func (GrepTool) Execute(ctx context.Context, sb sandbox.Sandbox, raw json.RawMessage) (Result, error) {
	var in grepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if in.Pattern == "" {
		return Result{Content: "pattern is required", IsError: true}, nil
	}
	path := in.Path
	if path == "" {
		path = "."
	}
	// 优先 ripgrep（有的话），fallback grep
	var cmd string
	if in.Glob != "" {
		cmd = fmt.Sprintf(
			"command -v rg >/dev/null && rg -n --no-heading --glob %s -- %s %s | head -200 || grep -rn --include=%s -E -- %s %s | head -200",
			shellQuote(in.Glob), shellQuote(in.Pattern), shellQuote(path),
			shellQuote(in.Glob), shellQuote(in.Pattern), shellQuote(path),
		)
	} else {
		cmd = fmt.Sprintf(
			"command -v rg >/dev/null && rg -n --no-heading -- %s %s | head -200 || grep -rn -E -- %s %s | head -200",
			shellQuote(in.Pattern), shellQuote(path),
			shellQuote(in.Pattern), shellQuote(path),
		)
	}
	res, err := sb.Exec(ctx, sandbox.Command{Args: []string{"bash", "-c", cmd}})
	if err != nil {
		return Result{Content: "exec error: " + err.Error(), IsError: true}, nil
	}
	if res.Stdout == "" {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: res.Stdout}, nil
}

// ─── shell helpers ────────────────────────────────────────────────────────

// shellQuote 单引号包裹字符串（处理内部单引号）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dirOf 返回路径的父目录（不依赖 path/filepath，避免 win 差异）。
func dirOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "."
	}
	return p[:i]
}
