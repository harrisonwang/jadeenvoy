package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

func TestResolveRefreshesExpiredMCPOAuthCredential(t *testing.T) {
	var refreshCalls int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type mismatch: %q", got)
		}
		if got := r.Form.Get("refresh_token"); got != "refresh-old" {
			t.Fatalf("refresh_token mismatch: %q", got)
		}
		if got := r.Form.Get("client_secret"); got != "client-secret" {
			t.Fatalf("client_secret mismatch: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-new",
			"refresh_token": "refresh-new",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(tokenSrv.Close)

	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "vault-oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc, err := New(st, "test-root-secret")
	if err != nil {
		t.Fatal(err)
	}

	v, err := svc.CreateVault(ctx, "tnt-default", apitypes.CreateVaultRequest{DisplayName: "OAuth"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.AddCredential(ctx, v.ID, "tnt-default", apitypes.CreateCredentialRequest{
		DisplayName: "oauth",
		Auth: apitypes.CredentialAuthInput{
			Type:         "mcp_oauth",
			MCPServerURL: "https://mcp.example.com",
			AccessToken:  "access-old",
			ExpiresAt:    time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			Refresh: &apitypes.CredentialOAuthRefreshInput{
				TokenEndpoint: tokenSrv.URL,
				ClientID:      "client-id",
				RefreshToken:  "refresh-old",
				TokenEndpointAuth: apitypes.CredentialOAuthEndpointAuthIn{
					Type:         "client_secret_post",
					ClientSecret: "client-secret",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := svc.Resolve(ctx, "tnt-default", []string{v.ID}, "mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Token != "access-new" || resolved.AuthType != "mcp_oauth" {
		t.Fatalf("unexpected resolved credential: %+v", resolved)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("expected one refresh call, got %d", got)
	}

	resolved, err = svc.Resolve(ctx, "tnt-default", []string{v.ID}, "mcp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Token != "access-new" {
		t.Fatalf("unexpected resolved credential after persist: %+v", resolved)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("second resolve should use persisted fresh token, got %d refreshes", got)
	}
}
