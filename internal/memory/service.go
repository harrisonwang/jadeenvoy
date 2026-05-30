// Package memory 是 Memory Store 的业务层 + 沙箱挂载逻辑。
//
// V1（M2）实现：基本 CRUD + 挂载（实物化到 sandbox 文件系统）。
// V2（M3）扩展：版本化 + CAS + redact。详见 .docs/30-adr/0014-memory-cas.md。
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}

// ─── Memory Store CRUD ────────────────────────────────────────────────────

type Store struct {
	Type        string     `json:"type"`
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `json:"archived_at"`
}

type CreateStoreRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (s *Service) CreateStore(ctx context.Context, tenantID string, req CreateStoreRequest) (*Store, error) {
	if req.Name == "" {
		return nil, errors.New("name is required")
	}
	r, err := s.st.CreateMemoryStore(ctx, store.CreateMemoryStoreInput{
		TenantID:    tenantID,
		Name:        req.Name,
		Description: req.Description,
	})
	if err != nil {
		return nil, err
	}
	return storeRowToAPI(r), nil
}

func (s *Service) GetStore(ctx context.Context, id string) (*Store, error) {
	r, err := s.st.GetMemoryStore(ctx, id)
	if err != nil {
		return nil, err
	}
	return storeRowToAPI(r), nil
}

func (s *Service) ListStores(ctx context.Context, tenantID string, limit int) ([]*Store, error) {
	rows, err := s.st.ListMemoryStores(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Store, 0, len(rows))
	for _, r := range rows {
		out = append(out, storeRowToAPI(r))
	}
	return out, nil
}

func (s *Service) DeleteStore(ctx context.Context, id string) error {
	return s.st.DeleteMemoryStore(ctx, id)
}

func storeRowToAPI(r *store.MemoryStoreRow) *Store {
	o := &Store{
		Type:      "memory_store",
		ID:        r.ID,
		Name:      r.Name,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.Description.Valid {
		o.Description = r.Description.String
	}
	return o
}

// ─── Memory CRUD ──────────────────────────────────────────────────────────

type Memory struct {
	Type          string    `json:"type"`
	ID            string    `json:"id"`
	MemoryStoreID string    `json:"memory_store_id"`
	Path          string    `json:"path"`
	Content       string    `json:"content,omitempty"` // 仅 retrieve 时返回
	ContentSha256 string    `json:"content_sha256"`
	ContentSize   int64     `json:"content_size"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type UpsertMemoryRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// V3: Precondition *Precondition `json:"precondition,omitempty"`
}

const MaxMemorySize = 100 * 1024 // 100 KB

func (s *Service) UpsertMemory(ctx context.Context, tenantID, storeID string, req UpsertMemoryRequest) (*Memory, error) {
	if req.Path == "" {
		return nil, errors.New("path is required")
	}
	if !strings.HasPrefix(req.Path, "/") {
		return nil, errors.New("path must be absolute (start with /)")
	}
	if len(req.Content) > MaxMemorySize {
		return nil, fmt.Errorf("content exceeds %d bytes limit", MaxMemorySize)
	}
	r, err := s.st.UpsertMemory(ctx, store.CreateMemoryInput{
		MemoryStoreID: storeID,
		TenantID:      tenantID,
		Path:          req.Path,
		Content:       req.Content,
	})
	if err != nil {
		return nil, err
	}
	return memRowToAPI(r, false), nil
}

func (s *Service) GetMemory(ctx context.Context, id string) (*Memory, error) {
	r, err := s.st.GetMemory(ctx, id)
	if err != nil {
		return nil, err
	}
	return memRowToAPI(r, true), nil
}

func (s *Service) ListMemories(ctx context.Context, storeID, pathPrefix string, limit int) ([]*Memory, error) {
	rows, err := s.st.ListMemories(ctx, storeID, pathPrefix, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Memory, 0, len(rows))
	for _, r := range rows {
		out = append(out, memRowToAPI(r, false))
	}
	return out, nil
}

func (s *Service) DeleteMemory(ctx context.Context, id string) error {
	return s.st.DeleteMemory(ctx, id)
}

func memRowToAPI(r *store.MemoryRow, includeContent bool) *Memory {
	o := &Memory{
		Type:          "memory",
		ID:            r.ID,
		MemoryStoreID: r.MemoryStoreID,
		Path:          r.Path,
		ContentSha256: r.ContentSha256,
		ContentSize:   r.ContentSize,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
	if includeContent {
		o.Content = r.Content
	}
	return o
}

// ─── Sandbox 挂载 ─────────────────────────────────────────────────────────

// MountSpec 描述一个 session 绑定的 memory store 挂载点。
type MountSpec struct {
	StoreID      string
	StoreName    string
	MountPath    string // 沙箱内挂载点，如 /mnt/memory/user-prefs/
	Access       string // read_only / read_write
	Instructions string // 给 agent 看的 instructions
}

// MountToSandbox 把 store 当前所有 memories 物化到 sandbox 挂载点。
//
// V1（M2）极简实现：直接写文件，不双向同步。Agent 在 sandbox 里修改这些文件
// 不会自动回写到 memory store —— 需要 agent 显式 POST 到 /v1/memory_stores/...
//
// V2 可改：用 fsnotify 监听 sandbox 写入 + 同步回 DB。
func (s *Service) MountToSandbox(ctx context.Context, sb sandbox.Sandbox, spec MountSpec) error {
	memories, err := s.st.ListMemories(ctx, spec.StoreID, "", 1000)
	if err != nil {
		return err
	}
	for _, mem := range memories {
		// path 是绝对路径如 /preferences/formatting.md
		// 沙箱内对应位置 = MountPath + path
		full := filepath.Join(spec.MountPath, strings.TrimPrefix(mem.Path, "/"))
		// 通过 bash 写入（沙箱接口只暴露 Exec）
		cmd := writeFileScript(full, mem.Content)
		res, err := sb.Exec(ctx, sandbox.Command{Args: []string{"bash", "-c", cmd}})
		if err != nil {
			return fmt.Errorf("write %s: %w", full, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("write %s: exit %d, %s", full, res.ExitCode, res.Stderr)
		}
	}
	return nil
}

// writeFileScript 生成把 content base64 写入 path 的 bash 命令。
func writeFileScript(path, content string) string {
	encoded := base64Encode(content)
	return fmt.Sprintf(`mkdir -p %s && echo %s | base64 -d > %s`,
		shellQuote(parentDir(path)),
		shellQuote(encoded),
		shellQuote(path),
	)
}

func parentDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "."
	}
	return p[:i]
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func base64Encode(s string) string {
	return stdEncoding.EncodeToString([]byte(s))
}

// SystemPromptHint 给 system prompt 加一段告诉 agent 哪些 memory store 已挂载。
func (s *Service) SystemPromptHint(specs []MountSpec) string {
	if len(specs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n# Available Memory Stores\n")
	sb.WriteString("The following memory stores are mounted as directories in your sandbox. ")
	sb.WriteString("Use file tools (read/write/edit) to interact with them.\n\n")
	for _, sp := range specs {
		fmt.Fprintf(&sb, "- **%s** at `%s` (%s)\n", sp.StoreName, sp.MountPath, sp.Access)
		if sp.Instructions != "" {
			fmt.Fprintf(&sb, "  %s\n", sp.Instructions)
		}
	}
	return sb.String()
}

// 避免 unused（json 在 V3 加 precondition 时会用）
var _ = json.Marshal
