package e2e

import (
	"strings"
	"testing"
)

// TestE2E_Environments_CRUD 验证 /v1/environments 增删查 + cloud 类型默认值（#6）。
func TestE2E_Environments_CRUD(t *testing.T) {
	srv, _ := setupHarness(t)

	// 创建 cloud environment
	var env map[string]any
	code := postJSON(t, srv, "/v1/environments", map[string]any{
		"name":   "prod-cloud",
		"config": map[string]any{"type": "cloud"},
	}, &env)
	if code != 201 {
		t.Fatalf("create environment: %d, body=%v", code, env)
	}
	envID, _ := env["id"].(string)
	if !strings.HasPrefix(envID, "env-") {
		t.Fatalf("expected env- id, got %v", env["id"])
	}
	if env["type"] != "environment" {
		t.Fatalf("expected type=environment, got %v", env["type"])
	}

	// Get
	var got map[string]any
	if code := getJSON(t, srv, "/v1/environments/"+envID, &got); code != 200 {
		t.Fatalf("get environment: %d", code)
	}
	if got["name"] != "prod-cloud" {
		t.Fatalf("expected name=prod-cloud, got %v", got["name"])
	}

	// Update
	code = postJSON(t, srv, "/v1/environments/"+envID, map[string]any{
		"name":   "prod-cloud-updated",
		"config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "limited"}},
	}, &got)
	if code != 200 {
		t.Fatalf("update environment: %d, body=%v", code, got)
	}
	if got["name"] != "prod-cloud-updated" {
		t.Fatalf("expected updated env name, got %v", got["name"])
	}

	// List
	var list map[string]any
	getJSON(t, srv, "/v1/environments", &list)
	if data, _ := list["data"].([]any); len(data) == 0 {
		t.Fatal("expected >=1 environment in list")
	}

	// 用该 environment 创建 session（校验 environment_id 必须存在）
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "env-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	code = postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent":          agentID,
		"environment_id": envID,
	}, &sess)
	if code != 201 {
		t.Fatalf("create session with env: %d, body=%v", code, sess)
	}
	if sess["environment_id"] != envID {
		t.Fatalf("expected session env_id=%s, got %v", envID, sess["environment_id"])
	}

	// Archive
	code = postJSON(t, srv, "/v1/environments/"+envID+"/archive", map[string]any{}, &got)
	if code != 200 {
		t.Fatalf("archive environment: %d, body=%v", code, got)
	}
	if got["archived_at"] == nil {
		t.Fatalf("expected archived_at after archive, got %v", got)
	}

	// 删除 environment
	resp, err := srv.Client().Do(mustReq(t, "DELETE", srv.URL+"/v1/environments/"+envID, nil))
	if err != nil {
		t.Fatalf("delete env: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 deleting env, got %d", resp.StatusCode)
	}
}

// TestE2E_Environments_SelfHostedNotImplemented 验证 self_hosted 明确 501（不静默 stub）。
func TestE2E_Environments_SelfHostedNotImplemented(t *testing.T) {
	srv, _ := setupHarness(t)
	var resp map[string]any
	code := postJSON(t, srv, "/v1/environments", map[string]any{
		"name":   "selfhosted",
		"config": map[string]any{"type": "self_hosted"},
	}, &resp)
	if code != 501 {
		t.Fatalf("expected 501 for self_hosted, got %d, body=%v", code, resp)
	}
}

// TestE2E_Session_UnknownEnvironment 验证引用不存在的 environment_id 时拒绝建 session。
func TestE2E_Session_UnknownEnvironment(t *testing.T) {
	srv, _ := setupHarness(t)
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "x-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	code := postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent":          agentID,
		"environment_id": "env-does-not-exist",
	}, &sess)
	if code != 400 && code != 404 {
		t.Fatalf("expected 400/404 for unknown environment, got %d, body=%v", code, sess)
	}
}

// TestE2E_Session_DefaultEnvironment 验证不传 environment_id 时回落到自动建的 default（向后兼容）。
func TestE2E_Session_DefaultEnvironment(t *testing.T) {
	srv, _ := setupHarness(t)
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name": "d-agent", "model": "mock-model", "system": "x",
		"tools": []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	code := postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	if code != 201 {
		t.Fatalf("create session without env should default, got %d, body=%v", code, sess)
	}
	if sess["environment_id"] != "default" {
		t.Fatalf("expected default environment, got %v", sess["environment_id"])
	}
}
