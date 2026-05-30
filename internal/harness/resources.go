package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/harrisonwang/jadeenvoy/internal/memory"
	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// mountResources 把 session 的 resources 物化到 sandbox，返回要追加到 system prompt 的提示文本。
func (h *Harness) mountResources(ctx context.Context, sb sandbox.Sandbox, sessionID string) (string, error) {
	resources, err := h.Store.ListSessionResources(ctx, sessionID)
	if err != nil {
		return "", err
	}
	var extrasParts []string
	var mounts []memory.MountSpec
	for _, res := range resources {
		switch res.Type {
		case "memory_store":
			mounts = append(mounts, h.parseMemoryResource(ctx, res)...)
		case "file":
			info, err := h.mountFileResource(ctx, sb, res)
			if err != nil {
				return "", err
			}
			if info != "" {
				extrasParts = append(extrasParts, info)
			}
		}
	}
	// Memory mounts（已有逻辑）
	if h.Memory != nil {
		for _, spec := range mounts {
			if err := h.Memory.MountToSandbox(ctx, sb, spec); err != nil {
				return "", err
			}
		}
		extrasParts = append(extrasParts, h.Memory.SystemPromptHint(mounts))
	}
	return strings.Join(extrasParts, "\n"), nil
}

// parseMemoryResource 解析 memory_store 类型的 resource。
func (h *Harness) parseMemoryResource(ctx context.Context, res *store.SessionResourceRow) []memory.MountSpec {
	if h.Memory == nil {
		return nil
	}
	var payload struct {
		MemoryStoreID string `json:"memory_store_id"`
		Access        string `json:"access"`
		Instructions  string `json:"instructions"`
	}
	_ = json.Unmarshal(res.Payload, &payload)
	if payload.MemoryStoreID == "" {
		return nil
	}
	st, err := h.Memory.GetStore(ctx, payload.MemoryStoreID)
	if err != nil {
		return nil
	}
	access := payload.Access
	if access == "" {
		access = "read_write"
	}
	mountPath := "/mnt/memory/" + sanitizeName(st.Name) + "/"
	return []memory.MountSpec{{
		StoreID:      st.ID,
		StoreName:    st.Name,
		MountPath:    mountPath,
		Access:       access,
		Instructions: payload.Instructions,
	}}
}

// mountFileResource 将 file 资源内容写入沙箱。
func (h *Harness) mountFileResource(ctx context.Context, sb sandbox.Sandbox, res *store.SessionResourceRow) (string, error) {
	var payload struct {
		FileID    string `json:"file_id"`
		MountPath string `json:"mount_path"`
	}
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		return "", fmt.Errorf("unmarshal file resource payload: %w", err)
	}
	if payload.FileID == "" || payload.MountPath == "" {
		return "", nil
	}
	fileRow, err := h.Store.GetFile(ctx, payload.FileID)
	if err != nil {
		return "", fmt.Errorf("get file %s: %w", payload.FileID, err)
	}
	if err := sb.WriteFile(ctx, payload.MountPath, fileRow.Blob); err != nil {
		return "", fmt.Errorf("write file to sandbox: %w", err)
	}
	return fmt.Sprintf("File %s is mounted at %s (%d bytes).", fileRow.Filename, payload.MountPath, fileRow.Size), nil
}

func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "store"
	}
	return string(out)
}
