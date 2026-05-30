package e2e

import "testing"

func TestE2E_AuthBypassRoutes(t *testing.T) {
	srv, _ := setupHarness(t)

	var sess map[string]any
	code := getJSON(t, srv, "/api/auth/session", &sess)
	if code != 200 {
		t.Fatalf("auth session: expected 200, got %d body=%v", code, sess)
	}
	user, _ := sess["user"].(map[string]any)
	if user["id"] != "usr-default" {
		t.Fatalf("expected default user, got %v", user)
	}

	var signup map[string]any
	code = postJSON(t, srv, "/api/auth/signup", map[string]any{
		"email":    "ignored@example.com",
		"password": "ignored",
	}, &signup)
	if code != 200 {
		t.Fatalf("auth signup bypass: expected 200, got %d body=%v", code, signup)
	}

	var login map[string]any
	code = postJSON(t, srv, "/api/auth/login", map[string]any{
		"email":    "ignored@example.com",
		"password": "ignored",
	}, &login)
	if code != 200 {
		t.Fatalf("auth login bypass: expected 200, got %d body=%v", code, login)
	}

	var logout map[string]any
	code = postJSON(t, srv, "/api/auth/logout", map[string]any{}, &logout)
	if code != 200 {
		t.Fatalf("auth logout: expected 200, got %d body=%v", code, logout)
	}
	if logout["success"] != true {
		t.Fatalf("expected success=true, got %v", logout)
	}
}
