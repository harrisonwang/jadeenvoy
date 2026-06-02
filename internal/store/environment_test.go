package store

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEnvironmentCRUD 覆盖 environment 增查删 + default 保护 + EnsureDefault 幂等。
func TestEnvironmentCRUD(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	env, err := st.CreateEnvironment(ctx, "tnt-default", "prod", json.RawMessage(`{"type":"cloud"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if env.Name != "prod" {
		t.Fatalf("name: %q", env.Name)
	}

	got, err := st.GetEnvironment(ctx, env.ID)
	if err != nil || got.ID != env.ID {
		t.Fatalf("get: %v / %v", err, got)
	}

	list, err := st.ListEnvironments(ctx, "tnt-default", 50)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	if err := st.DeleteEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetEnvironment(ctx, env.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestEnsureDefaultEnvironmentIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	for i := 0; i < 3; i++ {
		if err := st.EnsureDefaultEnvironment(ctx, "tnt-default"); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	got, err := st.GetEnvironment(ctx, "default")
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if got.ID != "default" {
		t.Fatalf("expected id=default, got %q", got.ID)
	}
	// default 不可删
	if err := st.DeleteEnvironment(ctx, "default"); err == nil {
		t.Fatal("expected error deleting default environment")
	}
}
