package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSubprocess_DestroyRemovesWorkdir 验证 Destroy 回收 session workdir（修复 workdir 泄漏）。
func TestSubprocess_DestroyRemovesWorkdir(t *testing.T) {
	root := t.TempDir()
	p := NewLocalSubprocessProvider(root)
	ctx := context.Background()

	sb, err := p.Provision(ctx, "sess-abc")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := sb.WriteFile(ctx, "/workspace/note.txt", []byte("hi")); err != nil {
		t.Fatalf("writefile: %v", err)
	}
	_ = sb.Close()

	workdir := filepath.Join(root, "sess-abc")
	if _, err := os.Stat(workdir); err != nil {
		t.Fatalf("workdir should exist before destroy: %v", err)
	}

	if err := p.Destroy(ctx, "sess-abc"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("workdir should be gone after destroy, stat err=%v", err)
	}
}

// TestSubprocess_DestroyKeepWorkdir 验证 KeepWorkdir=true 时 Destroy 保留目录（调试模式）。
func TestSubprocess_DestroyKeepWorkdir(t *testing.T) {
	root := t.TempDir()
	p := NewLocalSubprocessProvider(root)
	p.KeepWorkdir = true
	ctx := context.Background()

	if _, err := p.Provision(ctx, "sess-keep"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := p.Destroy(ctx, "sess-keep"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-keep")); err != nil {
		t.Fatalf("workdir should be kept when KeepWorkdir=true: %v", err)
	}
}

// TestSubprocess_DestroyRejectsBadSessionID 验证 Destroy 拒绝带路径分隔符的 id（防目录穿越）。
func TestSubprocess_DestroyRejectsBadSessionID(t *testing.T) {
	root := t.TempDir()
	p := NewLocalSubprocessProvider(root)
	if err := p.Destroy(context.Background(), "../../etc"); err == nil {
		t.Fatal("expected Destroy to reject session id containing path separators")
	}
}
