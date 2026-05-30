package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalSubprocessProvider 在主机上跑 subprocess，沙箱 = 一个独立 workdir。
//
// 警告：无任何隔离，仅适用于公司内部信任环境（详见 ADR-0010）。
type LocalSubprocessProvider struct {
	RootDir string // 比如 data/sandboxes/
}

func NewLocalSubprocessProvider(rootDir string) *LocalSubprocessProvider {
	return &LocalSubprocessProvider{RootDir: rootDir}
}

func (p *LocalSubprocessProvider) Provision(ctx context.Context, sessionID string) (Sandbox, error) {
	workdir := filepath.Join(p.RootDir, sessionID)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir workdir: %w", err)
	}
	return &subprocessSandbox{
		sessionID: sessionID,
		workdir:   workdir,
		env:       map[string]string{"JE_SESSION_ID": sessionID},
	}, nil
}

type subprocessSandbox struct {
	sessionID string
	workdir   string
	env       map[string]string
	mu        sync.Mutex
	closed    bool
}

func (s *subprocessSandbox) Exec(ctx context.Context, cmd Command) (*ExecResult, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("sandbox closed")
	}
	s.mu.Unlock()

	if len(cmd.Args) == 0 {
		return nil, fmt.Errorf("empty command args")
	}

	// 超时（默认 60s）
	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// V1 subprocess 沙箱无真隔离，把 Anthropic 风格的虚拟路径映射到 workdir 子目录。
	// 让 agent 用 "/workspace/file" 写文件能落到 host 上 <workdir>/workspace/file。
	args := make([]string, len(cmd.Args))
	for i, a := range cmd.Args {
		args[i] = s.rewriteVirtualPaths(a)
	}

	c := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	if cmd.Workdir != "" {
		c.Dir = filepath.Join(s.workdir, cmd.Workdir)
	} else {
		c.Dir = s.workdir
	}
	if cmd.Stdin != nil {
		c.Stdin = cmd.Stdin
	}

	// env: host env + sandbox 默认 env + per-command env
	envMap := map[string]string{}
	for _, kv := range os.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				envMap[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	for k, v := range s.env {
		envMap[k] = v
	}
	for k, v := range cmd.Env {
		envMap[k] = v
	}
	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}
	c.Env = envSlice

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			return &ExecResult{
				ExitCode: -1,
				Stdout:   stdout.String(),
				Stderr:   stderr.String() + "\n" + err.Error(),
			}, nil
		}
	}

	return &ExecResult{
		ExitCode: exit,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

func (s *subprocessSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	// V1 保留 workdir 便于调试。V2 加配置清理。
	return nil
}

// WriteFile 将内容写入沙箱 workdir 内的指定路径。
func (s *subprocessSandbox) WriteFile(ctx context.Context, path string, content []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("sandbox closed")
	}
	s.mu.Unlock()

	// rewriteVirtualPaths 已经返回含 workdir 的绝对路径
	resolved := s.rewriteVirtualPaths(path)
	// 确保不逃逸 workdir
	if !strings.HasPrefix(filepath.Clean(resolved), filepath.Clean(s.workdir)+string(filepath.Separator)) {
		return fmt.Errorf("path escapes sandbox: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(resolved, content, 0o644)
}

// SetEnv 给 sandbox 加默认 env（如 vault MITM 注入用的 HTTPS_PROXY）。
func (s *subprocessSandbox) SetEnv(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.env[k] = v
}

// rewriteVirtualPaths 把 Anthropic 风格的虚拟路径（/workspace、/mnt/memory、/mnt/session）
// 替换为 workdir 子路径。这是 subprocess 沙箱的简单模拟（V2 Docker 用真 mount）。
//
// 为减少误伤，只替换"路径起始"的虚拟前缀（前后是空白 / 引号 / 命令分隔符 / 字符串开头/结尾）。
func (s *subprocessSandbox) rewriteVirtualPaths(arg string) string {
	mappings := []struct {
		from string
		to   string
	}{
		{"/workspace", filepath.Join(s.workdir, "workspace")},
		{"/home/user/.skills", filepath.Join(s.workdir, "_home_user_skills")},
		{"/mnt/memory", filepath.Join(s.workdir, "_mnt_memory")},
		{"/mnt/session", filepath.Join(s.workdir, "_mnt_session")},
		{"/mnt/skills", filepath.Join(s.workdir, "_mnt_skills")},
	}
	for _, m := range mappings {
		arg = replaceAtBoundary(arg, m.from, m.to)
	}
	return arg
}

// replaceAtBoundary 替换字符串中所有"边界处"的 from。
//
// 边界 = 字符串开头/结尾、空白、引号、shell 分隔符等。
func replaceAtBoundary(s, from, to string) string {
	if !strings.Contains(s, from) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if i+len(from) <= len(s) && s[i:i+len(from)] == from {
			// 前边界检查
			leftOK := i == 0 || isPathBoundary(s[i-1])
			// 后边界：from 后必须是 path-char (/) 或 path 终止符
			var nextCh byte
			if i+len(from) < len(s) {
				nextCh = s[i+len(from)]
			}
			rightOK := nextCh == 0 || nextCh == '/' || isPathBoundary(nextCh)
			if leftOK && rightOK {
				b.WriteString(to)
				i += len(from)
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isPathBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '"', '\'', '`', '$', '(', ')', '|', '&', ';', '>', '<', ',':
		return true
	}
	return false
}
