package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

const credentialTokenLabel = "vault.credential.token"

// ErrUnsupportedAuthType 表示请求了 V1 不支持的凭据类型（如 mcp_oauth，见 ADR-0015）。
var ErrUnsupportedAuthType = errors.New("unsupported auth type")

// ErrConflict 透传 store 的唯一约束冲突（同 host 已有活跃凭据）。
var ErrConflict = store.ErrConflict

type Service struct {
	st  *store.Store
	box *cipherBox
}

// New 构造 Service。rootSecret 来自 PLATFORM_ROOT_SECRET；为空时用 dev 默认值
// （调用方负责告警），保证 dev / 测试不必强制配置。
func New(st *store.Store, rootSecret string) (*Service, error) {
	if rootSecret == "" {
		rootSecret = "jadeenvoy-dev-insecure-root-secret"
	}
	box, err := newCipherBox(rootSecret)
	if err != nil {
		return nil, err
	}
	return &Service{st: st, box: box}, nil
}

// ResolvedCredential 是解密后的凭据，仅供 MITM 注入路径使用。
type ResolvedCredential struct {
	CredentialID string
	AuthType     string
	Token        string
}

// ─── Vault CRUD ───────────────────────────────────────────────────────────

func (s *Service) CreateVault(ctx context.Context, tenantID string, req apitypes.CreateVaultRequest) (*apitypes.Vault, error) {
	if req.DisplayName == "" {
		return nil, fmt.Errorf("display_name is required")
	}
	meta, _ := json.Marshal(req.Metadata)
	if len(req.Metadata) == 0 {
		meta = []byte(`{}`)
	}
	row, err := s.st.CreateVault(ctx, tenantID, req.DisplayName, meta)
	if err != nil {
		return nil, err
	}
	return vaultToAPI(row), nil
}

func (s *Service) GetVault(ctx context.Context, tenantID, id string) (*apitypes.Vault, error) {
	row, err := s.st.GetVault(ctx, tenantID, id) // store 层按 tenant scoped，跨租户即 ErrNotFound
	if err != nil {
		return nil, err
	}
	return vaultToAPI(row), nil
}

func (s *Service) ListVaults(ctx context.Context, tenantID string, limit int) ([]*apitypes.Vault, error) {
	rows, err := s.st.ListVaults(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*apitypes.Vault, 0, len(rows))
	for _, r := range rows {
		out = append(out, vaultToAPI(r))
	}
	return out, nil
}

func (s *Service) ArchiveVault(ctx context.Context, tenantID, id string) error {
	return s.st.ArchiveVault(ctx, tenantID, id)
}

func (s *Service) DeleteVault(ctx context.Context, tenantID, id string) error {
	return s.st.DeleteVault(ctx, tenantID, id)
}

// ─── Credential ─────────────────────────────────────────────────────────────

func (s *Service) AddCredential(ctx context.Context, vaultID, tenantID string, req apitypes.CreateCredentialRequest) (*apitypes.Credential, error) {
	// V1 仅 static_bearer（ADR-0015）。mcp_oauth 由 API 层转 501。
	if req.Auth.Type != "static_bearer" {
		return nil, ErrUnsupportedAuthType
	}
	if req.Auth.Token == "" {
		return nil, fmt.Errorf("auth.token is required")
	}
	if req.Auth.MCPServerURL == "" {
		return nil, fmt.Errorf("auth.mcp_server_url is required")
	}
	host, err := hostFromURL(req.Auth.MCPServerURL)
	if err != nil {
		return nil, err
	}

	// 确认 vault 存在且属于该 tenant（store 层按 tenant scoped）
	if _, err := s.st.GetVault(ctx, tenantID, vaultID); err != nil {
		return nil, err
	}

	cipher, nonce, err := s.box.Encrypt([]byte(req.Auth.Token), credentialTokenLabel)
	if err != nil {
		return nil, err
	}

	row, err := s.st.CreateCredential(ctx, store.CreateCredentialInput{
		VaultID:       vaultID,
		TenantID:      tenantID,
		DisplayName:   req.DisplayName,
		AuthType:      "static_bearer",
		MCPServerURL:  req.Auth.MCPServerURL,
		MCPServerHost: host,
		Cipher:        cipher,
		CipherNonce:   nonce,
		CipherLabel:   credentialTokenLabel,
	})
	if err != nil {
		return nil, err
	}
	return credentialToAPI(row), nil
}

func (s *Service) ListCredentials(ctx context.Context, tenantID, vaultID string) ([]*apitypes.Credential, error) {
	// 先确认 vault 属于该租户（store 层按 tenant scoped），再列其凭据
	if _, err := s.st.GetVault(ctx, tenantID, vaultID); err != nil {
		return nil, err
	}
	rows, err := s.st.ListCredentialsByVault(ctx, tenantID, vaultID)
	if err != nil {
		return nil, err
	}
	out := make([]*apitypes.Credential, 0, len(rows))
	for _, r := range rows {
		out = append(out, credentialToAPI(r))
	}
	return out, nil
}

func (s *Service) DeleteCredential(ctx context.Context, tenantID, vaultID, id string) error {
	return s.st.DeleteCredential(ctx, tenantID, vaultID, id)
}

// Resolve 在给定 vault 集合里按 host 匹配活跃凭据，解密后返回（MITM 注入用）。
// 同 host 唯一约束保证至多一条；多条时取最新（created_at DESC）——规避 OMA 取最早的坑。
func (s *Service) Resolve(ctx context.Context, tenantID string, vaultIDs []string, host string) (*ResolvedCredential, error) {
	creds, err := s.st.ListActiveCredentialsByHost(ctx, tenantID, vaultIDs, host)
	if err != nil {
		return nil, err
	}
	for _, c := range creds {
		if c.AuthType != "static_bearer" {
			continue
		}
		token, err := s.box.Decrypt(c.Cipher, c.CipherNonce, c.CipherLabel)
		if err != nil {
			return nil, err
		}
		return &ResolvedCredential{
			CredentialID: c.ID,
			AuthType:     c.AuthType,
			Token:        string(token),
		}, nil
	}
	return nil, nil // 无匹配
}

// ─── helpers ─────────────────────────────────────────────────────────────

func hostFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid mcp_server_url: %w", err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("mcp_server_url has no host: %q", raw)
	}
	return u.Hostname(), nil
}

func vaultToAPI(r *store.VaultRow) *apitypes.Vault {
	out := &apitypes.Vault{
		Type:        "vault",
		ID:          r.ID,
		DisplayName: r.DisplayName,
		Metadata:    map[string]string{},
		CreatedAt:   r.CreatedAt,
	}
	u := r.UpdatedAt
	out.UpdatedAt = &u
	_ = json.Unmarshal(r.Metadata, &out.Metadata)
	if r.ArchivedAt.Valid {
		at := time.UnixMilli(r.ArchivedAt.Int64).UTC()
		out.ArchivedAt = &at
	}
	return out
}

func credentialToAPI(r *store.CredentialRow) *apitypes.Credential {
	return &apitypes.Credential{
		Type:        "credential",
		ID:          r.ID,
		VaultID:     r.VaultID,
		DisplayName: r.DisplayName,
		Auth: apitypes.CredentialAuthView{
			Type:         r.AuthType,
			MCPServerURL: r.MCPServerURL,
		},
		CreatedAt: r.CreatedAt,
	}
}
