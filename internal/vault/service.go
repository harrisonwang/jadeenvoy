package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

const credentialTokenLabel = "vault.credential.token"
const credentialOAuthLabel = "vault.credential.oauth"

// ErrUnsupportedAuthType 表示请求了未知凭据类型。
var ErrUnsupportedAuthType = errors.New("unsupported auth type")

// ErrConflict 透传 store 的唯一约束冲突（同 host 已有活跃凭据）。
var ErrConflict = store.ErrConflict

type Service struct {
	st         *store.Store
	box        *cipherBox
	httpClient *http.Client
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
	return &Service{st: st, box: box, httpClient: &http.Client{Timeout: 10 * time.Second}}, nil
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

func (s *Service) UpdateVault(ctx context.Context, tenantID, id string, req apitypes.CreateVaultRequest) (*apitypes.Vault, error) {
	if req.DisplayName == "" {
		return nil, fmt.Errorf("display_name is required")
	}
	meta, _ := json.Marshal(req.Metadata)
	if len(req.Metadata) == 0 {
		meta = []byte(`{}`)
	}
	row, err := s.st.UpdateVault(ctx, tenantID, id, req.DisplayName, meta)
	if err != nil {
		return nil, err
	}
	return vaultToAPI(row), nil
}

func (s *Service) DeleteVault(ctx context.Context, tenantID, id string) error {
	return s.st.DeleteVault(ctx, tenantID, id)
}

// ─── Credential ─────────────────────────────────────────────────────────────

func (s *Service) AddCredential(ctx context.Context, vaultID, tenantID string, req apitypes.CreateCredentialRequest) (*apitypes.Credential, error) {
	if req.Auth.Type != "static_bearer" && req.Auth.Type != "mcp_oauth" {
		return nil, ErrUnsupportedAuthType
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

	cipher, nonce, label, err := s.encryptCredentialAuth(req.Auth)
	if err != nil {
		return nil, err
	}

	row, err := s.st.CreateCredential(ctx, store.CreateCredentialInput{
		VaultID:       vaultID,
		TenantID:      tenantID,
		DisplayName:   req.DisplayName,
		AuthType:      req.Auth.Type,
		MCPServerURL:  req.Auth.MCPServerURL,
		MCPServerHost: host,
		Cipher:        cipher,
		CipherNonce:   nonce,
		CipherLabel:   label,
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

func (s *Service) GetCredential(ctx context.Context, tenantID, vaultID, id string) (*apitypes.Credential, error) {
	if _, err := s.st.GetVault(ctx, tenantID, vaultID); err != nil {
		return nil, err
	}
	row, err := s.st.GetCredentialScoped(ctx, tenantID, vaultID, id)
	if err != nil {
		return nil, err
	}
	return credentialToAPI(row), nil
}

func (s *Service) UpdateCredential(ctx context.Context, tenantID, vaultID, id string, req apitypes.CreateCredentialRequest) (*apitypes.Credential, error) {
	if req.Auth.Type != "" && req.Auth.Type != "static_bearer" && req.Auth.Type != "mcp_oauth" {
		return nil, ErrUnsupportedAuthType
	}
	if req.Auth.MCPServerURL == "" {
		return nil, fmt.Errorf("auth.mcp_server_url is required")
	}
	host, err := hostFromURL(req.Auth.MCPServerURL)
	if err != nil {
		return nil, err
	}
	if _, err := s.st.GetVault(ctx, tenantID, vaultID); err != nil {
		return nil, err
	}
	current, err := s.st.GetCredentialScoped(ctx, tenantID, vaultID, id)
	if err != nil {
		return nil, err
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = current.DisplayName
	}
	cipher := current.Cipher
	nonce := current.CipherNonce
	label := current.CipherLabel
	authType := req.Auth.Type
	if authType == "" {
		authType = current.AuthType
	}
	if req.Auth.Token != "" || req.Auth.AccessToken != "" || req.Auth.Refresh != nil || req.Auth.ExpiresAt != "" || authType != current.AuthType {
		req.Auth.Type = authType
		cipher, nonce, label, err = s.encryptCredentialAuth(req.Auth)
		if err != nil {
			return nil, err
		}
	}
	row, err := s.st.UpdateCredential(ctx, store.UpdateCredentialInput{
		ID:            id,
		VaultID:       vaultID,
		TenantID:      tenantID,
		DisplayName:   displayName,
		AuthType:      authType,
		MCPServerURL:  req.Auth.MCPServerURL,
		MCPServerHost: host,
		Cipher:        cipher,
		CipherNonce:   nonce,
		CipherLabel:   label,
	})
	if err != nil {
		return nil, err
	}
	return credentialToAPI(row), nil
}

func (s *Service) ArchiveCredential(ctx context.Context, tenantID, vaultID, id string) error {
	if _, err := s.st.GetVault(ctx, tenantID, vaultID); err != nil {
		return err
	}
	return s.st.ArchiveCredential(ctx, tenantID, vaultID, id)
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
		switch c.AuthType {
		case "static_bearer":
			token, err := s.box.Decrypt(c.Cipher, c.CipherNonce, c.CipherLabel)
			if err != nil {
				return nil, err
			}
			return &ResolvedCredential{
				CredentialID: c.ID,
				AuthType:     c.AuthType,
				Token:        string(token),
			}, nil
		case "mcp_oauth":
			secret, err := s.decryptOAuthSecret(c)
			if err != nil {
				return nil, err
			}
			if s.oauthNeedsRefresh(secret, time.Now().UTC()) && secret.Refresh != nil {
				refreshed, err := s.refreshOAuth(ctx, secret)
				if err != nil {
					return nil, err
				}
				if err := s.persistOAuthSecret(ctx, c, refreshed); err != nil {
					return nil, err
				}
				secret = refreshed
			}
			return &ResolvedCredential{
				CredentialID: c.ID,
				AuthType:     c.AuthType,
				Token:        secret.AccessToken,
			}, nil
		default:
			continue
		}
	}
	return nil, nil // 无匹配
}

func (s *Service) ValidateOAuthCredential(ctx context.Context, tenantID, vaultID, id string) error {
	row, err := s.st.GetCredentialScoped(ctx, tenantID, vaultID, id)
	if err != nil {
		return err
	}
	if row.AuthType != "mcp_oauth" {
		return fmt.Errorf("credential is not mcp_oauth")
	}
	_, err = s.decryptOAuthSecret(row)
	return err
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

type oauthSecret struct {
	AccessToken string                                `json:"access_token"`
	ExpiresAt   string                                `json:"expires_at,omitempty"`
	Refresh     *apitypes.CredentialOAuthRefreshInput `json:"refresh,omitempty"`
}

func (s *Service) encryptCredentialAuth(auth apitypes.CredentialAuthInput) ([]byte, []byte, string, error) {
	switch auth.Type {
	case "static_bearer":
		if auth.Token == "" {
			return nil, nil, "", fmt.Errorf("auth.token is required")
		}
		cipher, nonce, err := s.box.Encrypt([]byte(auth.Token), credentialTokenLabel)
		return cipher, nonce, credentialTokenLabel, err
	case "mcp_oauth":
		secret, err := oauthSecretFromInput(auth)
		if err != nil {
			return nil, nil, "", err
		}
		raw, _ := json.Marshal(secret)
		cipher, nonce, err := s.box.Encrypt(raw, credentialOAuthLabel)
		return cipher, nonce, credentialOAuthLabel, err
	default:
		return nil, nil, "", ErrUnsupportedAuthType
	}
}

func oauthSecretFromInput(auth apitypes.CredentialAuthInput) (oauthSecret, error) {
	if auth.AccessToken == "" {
		return oauthSecret{}, fmt.Errorf("auth.access_token is required")
	}
	if auth.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, auth.ExpiresAt); err != nil {
			return oauthSecret{}, fmt.Errorf("auth.expires_at must be RFC3339: %w", err)
		}
	}
	if auth.Refresh != nil {
		if auth.Refresh.TokenEndpoint == "" {
			return oauthSecret{}, fmt.Errorf("auth.refresh.token_endpoint is required")
		}
		if auth.Refresh.RefreshToken == "" {
			return oauthSecret{}, fmt.Errorf("auth.refresh.refresh_token is required")
		}
		switch auth.Refresh.TokenEndpointAuth.Type {
		case "", "none", "client_secret_basic", "client_secret_post":
		default:
			return oauthSecret{}, fmt.Errorf("unsupported token_endpoint_auth.type %q", auth.Refresh.TokenEndpointAuth.Type)
		}
		if auth.Refresh.TokenEndpointAuth.Type == "" {
			auth.Refresh.TokenEndpointAuth.Type = "none"
		}
	}
	return oauthSecret{AccessToken: auth.AccessToken, ExpiresAt: auth.ExpiresAt, Refresh: auth.Refresh}, nil
}

func (s *Service) decryptOAuthSecret(row *store.CredentialRow) (oauthSecret, error) {
	raw, err := s.box.Decrypt(row.Cipher, row.CipherNonce, row.CipherLabel)
	if err != nil {
		return oauthSecret{}, err
	}
	var secret oauthSecret
	if err := json.Unmarshal(raw, &secret); err != nil {
		return oauthSecret{}, err
	}
	return secret, nil
}

func (s *Service) oauthNeedsRefresh(secret oauthSecret, now time.Time) bool {
	if secret.ExpiresAt == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, secret.ExpiresAt)
	if err != nil {
		return false
	}
	return !now.Add(time.Minute).Before(expiresAt)
}

func (s *Service) refreshOAuth(ctx context.Context, secret oauthSecret) (oauthSecret, error) {
	if secret.Refresh == nil {
		return secret, nil
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", secret.Refresh.RefreshToken)
	if secret.Refresh.ClientID != "" {
		form.Set("client_id", secret.Refresh.ClientID)
	}
	if secret.Refresh.Scope != "" {
		form.Set("scope", secret.Refresh.Scope)
	}
	auth := secret.Refresh.TokenEndpointAuth
	switch auth.Type {
	case "client_secret_post":
		form.Set("client_secret", auth.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, secret.Refresh.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthSecret{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth.Type == "client_secret_basic" {
		req.SetBasicAuth(secret.Refresh.ClientID, auth.ClientSecret)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return oauthSecret{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthSecret{}, fmt.Errorf("oauth refresh failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		ExpiresAt    string `json:"expires_at"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return oauthSecret{}, err
	}
	if out.AccessToken == "" {
		return oauthSecret{}, fmt.Errorf("oauth refresh response missing access_token")
	}
	secret.AccessToken = out.AccessToken
	if out.RefreshToken != "" {
		secret.Refresh.RefreshToken = out.RefreshToken
	}
	if out.Scope != "" {
		secret.Refresh.Scope = out.Scope
	}
	switch {
	case out.ExpiresIn > 0:
		secret.ExpiresAt = time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second).Format(time.RFC3339)
	case out.ExpiresAt != "":
		if _, err := time.Parse(time.RFC3339, out.ExpiresAt); err != nil {
			return oauthSecret{}, fmt.Errorf("oauth refresh response expires_at must be RFC3339: %w", err)
		}
		secret.ExpiresAt = out.ExpiresAt
	default:
		secret.ExpiresAt = ""
	}
	return secret, nil
}

func (s *Service) persistOAuthSecret(ctx context.Context, row *store.CredentialRow, secret oauthSecret) error {
	raw, _ := json.Marshal(secret)
	cipher, nonce, err := s.box.Encrypt(raw, credentialOAuthLabel)
	if err != nil {
		return err
	}
	_, err = s.st.UpdateCredential(ctx, store.UpdateCredentialInput{
		ID:            row.ID,
		VaultID:       row.VaultID,
		TenantID:      row.TenantID,
		DisplayName:   row.DisplayName,
		AuthType:      row.AuthType,
		MCPServerURL:  row.MCPServerURL,
		MCPServerHost: row.MCPServerHost,
		Cipher:        cipher,
		CipherNonce:   nonce,
		CipherLabel:   credentialOAuthLabel,
	})
	return err
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
	out := &apitypes.Credential{
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
	u := r.UpdatedAt
	out.UpdatedAt = &u
	if r.ArchivedAt.Valid {
		at := time.UnixMilli(r.ArchivedAt.Int64).UTC()
		out.ArchivedAt = &at
	}
	return out
}
