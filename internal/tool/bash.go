package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
)

// BashTool 是 "bash" 工具。
type BashTool struct{}

func (BashTool) Name() string { return "bash" }

func (BashTool) Description() string {
	return "Execute a shell command in the sandbox. Returns stdout, stderr, and exit code."
}

func (BashTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The shell command to execute"},
			"timeout": {"type": "integer", "description": "Timeout in milliseconds (default 60000)"}
		},
		"required": ["command"]
	}`)
}

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func (BashTool) Execute(ctx context.Context, sb sandbox.Sandbox, input json.RawMessage) (Result, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if in.Command == "" {
		return Result{Content: "command is required", IsError: true}, nil
	}

	timeout := time.Duration(in.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	res, err := sb.Exec(ctx, sandbox.Command{
		Args:    []string{"bash", "-c", in.Command},
		Timeout: timeout,
	})
	if err != nil {
		return Result{Content: "exec error: " + err.Error(), IsError: true}, nil
	}

	var sb2 strings.Builder
	fmt.Fprintf(&sb2, "exit=%d\n%s", res.ExitCode, res.Stdout)
	if res.Stderr != "" {
		fmt.Fprintf(&sb2, "\nstderr: %s", res.Stderr)
	}
	return Result{
		Content: sb2.String(),
		IsError: res.ExitCode != 0,
	}, nil
}
