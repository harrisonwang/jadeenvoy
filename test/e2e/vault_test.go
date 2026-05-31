package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/harrisonwang/jadeenvoy/internal/api"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/vault"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

func setupVaultAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	tmp := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, "sqlite://"+filepath.Join(tmp, "vault.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	vsvc, err := vault.New(st, "test-root-secret")
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	deps := &api.Deps{Store: st, Vault: vsvc, AuthMode: "bypass"}
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	return srv
}

// TestE2E_VaultCRUD 覆盖 vault + credential CRUD、secret 剥离、同 host 409、
// mcp_oauth 501（ADR-0015）。
func TestE2E_VaultCRUD(t *testing.T) {
	srv := setupVaultAPIServer(t)

	var v map[string]any
	if code := postJSON(t, srv, "/v1/vaults", map[string]any{"display_name": "GitLab"}, &v); code != 201 {
		t.Fatalf("create vault: %d %v", code, v)
	}
	vid, _ := v["id"].(string)
	if vid == "" || v["type"] != "vault" {
		t.Fatalf("bad vault: %v", v)
	}

	// add credential
	var c map[string]any
	code := postJSON(t, srv, "/v1/vaults/"+vid+"/credentials", map[string]any{
		"display_name": "PAT",
		"auth":         map[string]any{"type": "static_bearer", "mcp_server_url": "https://git.example.com", "token": "glpat-xxx"},
	}, &c)
	if code != 201 {
		t.Fatalf("add credential: %d %v", code, c)
	}
	// secret 必须剥离
	authView, _ := c["auth"].(map[string]any)
	if _, leaked := authView["token"]; leaked {
		t.Fatalf("token leaked in credential response: %v", c)
	}
	if authView["mcp_server_url"] != "https://git.example.com" {
		t.Fatalf("bad auth view: %v", authView)
	}

	// list（仍不含 secret）
	var list map[string]any
	if code := getJSON(t, srv, "/v1/vaults/"+vid+"/credentials", &list); code != 200 {
		t.Fatalf("list credentials: %d", code)
	}
	if data, _ := list["data"].([]any); len(data) != 1 {
		t.Fatalf("expected 1 credential, got %v", list["data"])
	}

	// 同 host 第二条 → 409（唯一约束，规避 OMA 取最早凭据的坑）
	var conflict map[string]any
	code = postJSON(t, srv, "/v1/vaults/"+vid+"/credentials", map[string]any{
		"display_name": "PAT2",
		"auth":         map[string]any{"type": "static_bearer", "mcp_server_url": "https://git.example.com/sub", "token": "glpat-yyy"},
	}, &conflict)
	if code != 409 {
		t.Fatalf("expected 409 on duplicate host, got %d %v", code, conflict)
	}

	// mcp_oauth → 501
	var oauth map[string]any
	code = postJSON(t, srv, "/v1/vaults/"+vid+"/credentials", map[string]any{
		"display_name": "oauth",
		"auth":         map[string]any{"type": "mcp_oauth", "mcp_server_url": "https://api.example.com", "token": "t"},
	}, &oauth)
	if code != 501 {
		t.Fatalf("expected 501 for mcp_oauth, got %d %v", code, oauth)
	}

	// delete vault
	req := mustReq(t, "DELETE", srv.URL+"/v1/vaults/"+vid, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("delete vault: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete vault: expected 200, got %d", resp.StatusCode)
	}
}

// TestE2E_VaultMITMInjection 端到端验证 MITM 注入：起一个回显 Authorization 的
// HTTPS 后端，client 经 je-vault 代理访问，断言 dummy 凭据被剥离、真 token 注入。
func TestE2E_VaultMITMInjection(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(tmp, "mitm.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	vsvc, err := vault.New(st, "test-root-secret")
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}

	// 假 "GitLab" HTTPS 后端，回显收到的 Authorization
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)

	// vault + credential（host = 127.0.0.1）
	v, err := vsvc.CreateVault(ctx, "tnt-default", apitypes.CreateVaultRequest{DisplayName: "GitLab"})
	if err != nil {
		t.Fatalf("create vault: %v", err)
	}
	if _, err := vsvc.AddCredential(ctx, v.ID, "tnt-default", apitypes.CreateCredentialRequest{
		DisplayName: "PAT",
		Auth:        apitypes.CredentialAuthInput{Type: "static_bearer", MCPServerURL: "https://" + backendURL.Host, Token: "secret-pat-token"},
	}); err != nil {
		t.Fatalf("add credential: %v", err)
	}

	// agent + session 绑 vault
	a, err := st.CreateAgent(ctx, store.CreateAgentInput{Name: "a", Model: json.RawMessage(`"mock"`)})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	sess, err := st.CreateSession(ctx, store.CreateSessionInput{
		AgentID: a.ID, AgentVersion: a.Version, AgentSnapshot: json.RawMessage(`{}`), VaultIDs: []string{v.ID},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	inject := func(ctx context.Context, sessionID, host string) (string, bool) {
		s, err := st.GetSession(ctx, sessionID)
		if err != nil {
			return "", false
		}
		var ids []string
		_ = json.Unmarshal(s.VaultIDs, &ids)
		rc, err := vsvc.Resolve(ctx, s.TenantID, ids, host)
		if err != nil || rc == nil {
			return "", false
		}
		return rc.Token, true
	}

	ca, err := vault.LoadOrCreateCA(filepath.Join(tmp, "ca"))
	if err != nil {
		t.Fatalf("CA: %v", err)
	}
	// 代理 → 后端用 InsecureSkipVerify（后端是 httptest 自签）
	proxy := vault.NewProxy(ca, inject, &tls.Config{InsecureSkipVerify: true})
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// client：信任代理 CA，HTTPS_PROXY 带 session（userinfo）
	caPEM, _ := os.ReadFile(ca.CertPath)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("append CA failed")
	}
	pURL, _ := url.Parse(proxyServer.URL)
	pURL.User = url.UserPassword(sess.ID, "x")
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pURL),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}

	req, _ := http.NewRequest("GET", backend.URL, nil)
	req.Header.Set("Authorization", "Bearer dummy-client-token") // 应被剥离
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Bearer secret-pat-token" {
		t.Fatalf("expected injected vault token, got %q", body)
	}
}

// TestVault_TenantIsolation 回归：一个租户不能读/删/写另一个租户的 vault 与凭据。
func TestVault_TenantIsolation(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://"+filepath.Join(tmp, "iso.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	vsvc, _ := vault.New(st, "test-root-secret")

	// 租户 A 建 vault + 凭据
	vA, err := vsvc.CreateVault(ctx, "tnt-A", apitypes.CreateVaultRequest{DisplayName: "A's vault"})
	if err != nil {
		t.Fatalf("create vault A: %v", err)
	}
	credA, err := vsvc.AddCredential(ctx, vA.ID, "tnt-A", apitypes.CreateCredentialRequest{
		DisplayName: "A PAT",
		Auth:        apitypes.CredentialAuthInput{Type: "static_bearer", MCPServerURL: "https://git.example.com", Token: "secret"},
	})
	if err != nil {
		t.Fatalf("add credential A: %v", err)
	}

	// 租户 B 不能读 / 删 / 列 / 写 A 的 vault —— 一律 ErrNotFound（不泄漏存在性）
	if _, err := vsvc.GetVault(ctx, "tnt-B", vA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B GetVault(A) should be ErrNotFound, got %v", err)
	}
	if err := vsvc.DeleteVault(ctx, "tnt-B", vA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B DeleteVault(A) should be ErrNotFound, got %v", err)
	}
	if err := vsvc.ArchiveVault(ctx, "tnt-B", vA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B ArchiveVault(A) should be ErrNotFound, got %v", err)
	}
	if _, err := vsvc.ListCredentials(ctx, "tnt-B", vA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B ListCredentials(A) should be ErrNotFound, got %v", err)
	}
	if _, err := vsvc.AddCredential(ctx, vA.ID, "tnt-B", apitypes.CreateCredentialRequest{
		DisplayName: "evil", Auth: apitypes.CredentialAuthInput{Type: "static_bearer", MCPServerURL: "https://evil.example.com", Token: "x"},
	}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B AddCredential(A) should be ErrNotFound, got %v", err)
	}
	if err := vsvc.DeleteCredential(ctx, "tnt-B", vA.ID, credA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("B DeleteCredential(A) should be ErrNotFound, got %v", err)
	}

	// 租户 A 自己仍可正常访问
	if _, err := vsvc.GetVault(ctx, "tnt-A", vA.ID); err != nil {
		t.Errorf("A GetVault(A) should succeed, got %v", err)
	}
	if creds, err := vsvc.ListCredentials(ctx, "tnt-A", vA.ID); err != nil || len(creds) != 1 {
		t.Errorf("A ListCredentials(A) should return 1, got %v err=%v", len(creds), err)
	}
}
