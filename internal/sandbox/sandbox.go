// Package sandbox 是工具执行环境抽象。
// V1 仅实现 LocalSubprocess。
package sandbox

import (
	"context"
	"io"
	"time"
)

// Command 是 sandbox 内执行的命令描述。
type Command struct {
	Args    []string
	Stdin   io.Reader
	Env     map[string]string
	Workdir string // 相对沙箱根；为空 = 沙箱根
	Timeout time.Duration
}

// ExecResult 是命令执行结果。
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// V2: TruncatedOutputFile string
}

// Sandbox 是工具执行接口。
type Sandbox interface {
	Exec(ctx context.Context, cmd Command) (*ExecResult, error)
	WriteFile(ctx context.Context, path string, content []byte) error
	// SetEnv 设置沙箱默认环境变量（如 vault MITM 注入的 HTTPS_PROXY / CA）。
	SetEnv(key, value string)
	Close() error
}

// Provider 给 session 分配 sandbox。
type Provider interface {
	Provision(ctx context.Context, sessionID string) (Sandbox, error)
	// Destroy 回收 session 的持久化沙箱资源（如 workdir）。session 删除时调用。
	// 对不存在的 session 应返回 nil（幂等）。
	Destroy(ctx context.Context, sessionID string) error
}
