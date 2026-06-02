package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestPostgresStoreSmoke(t *testing.T) {
	dbURL := os.Getenv("JE_TEST_POSTGRES_URL")
	if dbURL == "" {
		t.Skip("set JE_TEST_POSTGRES_URL to run postgres store smoke test")
	}

	ctx := context.Background()
	st, err := Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if st.Driver != "postgres" {
		t.Fatalf("expected postgres driver, got %s", st.Driver)
	}

	tenantID := "tnt-pg-smoke"
	env, err := st.CreateEnvironment(ctx, tenantID, "pg-smoke", json.RawMessage(`{"type":"cloud"}`))
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteEnvironment(ctx, env.ID) })

	agent, err := st.CreateAgent(ctx, CreateAgentInput{
		TenantID: tenantID,
		Name:     "pg-smoke-agent",
		Model:    json.RawMessage(`{"id":"tw-agent-max"}`),
		System:   "postgres smoke",
		Tools:    json.RawMessage(`[]`),
		Metadata: json.RawMessage(`{"suite":"postgres"}`),
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteAgent(ctx, agent.ID) })

	session, err := st.CreateSession(ctx, CreateSessionInput{
		TenantID:      tenantID,
		AgentID:       agent.ID,
		AgentVersion:  agent.Version,
		AgentSnapshot: json.RawMessage(`{"id":"` + agent.ID + `","model":{"id":"tw-agent-max"}}`),
		EnvironmentID: env.ID,
		Metadata:      json.RawMessage(`{"suite":"postgres"}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteSession(ctx, session.ID) })

	if _, err := st.AppendEvent(ctx, AppendEventInput{
		SessionID: session.ID,
		Type:      "user.message",
		Payload:   json.RawMessage(`{"type":"user.message","content":[{"type":"text","text":"hello pg"}]}`),
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	events, err := st.ListEvents(ctx, session.ID, []string{"user.message"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("unexpected events: %+v", events)
	}
}
