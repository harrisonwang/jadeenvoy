package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harrisonwang/jadeenvoy/internal/agent"
	"github.com/harrisonwang/jadeenvoy/internal/api"
	"github.com/harrisonwang/jadeenvoy/internal/auth"
	"github.com/harrisonwang/jadeenvoy/internal/session"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

func setupAuthAPIServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	tmp := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, "sqlite://"+filepath.Join(tmp, "auth.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	deps := &api.Deps{
		Store:    st,
		Agent:    agent.New(st),
		Session:  session.New(st),
		Auth:     auth.New(st, mode, []byte("test-secret")),
		AuthMode: mode,
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	return srv
}

// TestE2E_AuthRequiredMode 覆盖 required 模式下 signup→cookie→访问受保护端点，
// 以及无凭据 401、错误密码 401、重复 email 409（ADR-0013）。
func TestE2E_AuthRequiredMode(t *testing.T) {
	srv := setupAuthAPIServer(t, "required")

	// 无凭据访问 /v1 → 401
	resp, err := http.Get(srv.URL + "/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// signup → 200 + Set-Cookie
	signupBody, _ := json.Marshal(map[string]any{"email": "a@b.com", "password": "hunter2pw", "name": "A"})
	resp, err = client.Post(srv.URL+"/api/auth/signup", "application/json", bytes.NewReader(signupBody))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("signup: expected 200, got %d %s", resp.StatusCode, raw)
	}
	u, _ := url.Parse(srv.URL)
	if len(jar.Cookies(u)) == 0 {
		t.Fatalf("no session cookie set after signup")
	}

	// 带 cookie 访问 /v1 → 200
	resp, _ = client.Get(srv.URL + "/v1/agents")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 with cookie, got %d", resp.StatusCode)
	}

	// /api/auth/session 返回真实 user
	resp, _ = client.Get(srv.URL + "/api/auth/session")
	var sess map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()
	if user, _ := sess["user"].(map[string]any); user["email"] != "a@b.com" {
		t.Fatalf("bad session user: %v", sess)
	}

	// 错误密码 → 401
	badBody, _ := json.Marshal(map[string]any{"email": "a@b.com", "password": "wrong"})
	resp, _ = http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(badBody))
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 for bad password, got %d", resp.StatusCode)
	}

	// 重复 email signup → 409
	resp, _ = http.Post(srv.URL+"/api/auth/signup", "application/json", bytes.NewReader(signupBody))
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("expected 409 for duplicate email, got %d", resp.StatusCode)
	}
}

// TestE2E_APIKeyAuth 覆盖 API key 签发 + x-api-key 鉴权 + 错误 key 401（ADR-0013）。
func TestE2E_APIKeyAuth(t *testing.T) {
	srv := setupAuthAPIServer(t, "required")

	// 注册拿 cookie（管理员）
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signupBody, _ := json.Marshal(map[string]any{"email": "admin@b.com", "password": "hunter2pw"})
	resp, err := client.Post(srv.URL+"/api/auth/signup", "application/json", bytes.NewReader(signupBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 用 cookie 在 /admin 创建 api key
	resp, err = client.Post(srv.URL+"/admin/api_keys", "application/json", strings.NewReader(`{"name":"ci"}`))
	if err != nil {
		t.Fatal(err)
	}
	var key map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&key)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create api key: expected 201, got %d %v", resp.StatusCode, key)
	}
	plaintext, _ := key["key"].(string)
	if !strings.HasPrefix(plaintext, "je_") {
		t.Fatalf("bad api key: %v", key)
	}

	// 用 x-api-key 访问 /v1 → 200
	req := mustReq(t, "GET", srv.URL+"/v1/agents", nil)
	req.Header.Set("x-api-key", plaintext)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("x-api-key auth: expected 200, got %d", resp.StatusCode)
	}

	// 错误 key → 401
	req = mustReq(t, "GET", srv.URL+"/v1/agents", nil)
	req.Header.Set("x-api-key", "je_bogus")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("bad api key: expected 401, got %d", resp.StatusCode)
	}
}
