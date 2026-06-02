package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// makeSession 建一个最小可用的 agent+session，返回 session id。
func makeSession(t *testing.T, st *Store) string {
	t.Helper()
	ctx := context.Background()
	a, err := st.CreateAgent(ctx, CreateAgentInput{
		Name: "a", Model: json.RawMessage(`"m"`), Tools: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	sess, err := st.CreateSession(ctx, CreateSessionInput{
		AgentID: a.ID, AgentVersion: a.Version, AgentSnapshot: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess.ID
}

// TestRecoverInterruptedSessions 验证 ListSessionsByStatus + MarkSessionTerminated 配合
// 能把 running 的 session 收敛成 terminated（启动恢复 sweep 的核心，task F）。
func TestRecoverInterruptedSessions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	running := makeSession(t, st)
	idle := makeSession(t, st)

	if err := st.UpdateSessionStatus(ctx, running, "running"); err != nil {
		t.Fatalf("set running: %v", err)
	}
	// idle 保持默认 idle

	ids, err := st.ListSessionsByStatus(ctx, "running", "rescheduling")
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(ids) != 1 || ids[0] != running {
		t.Fatalf("expected [%s], got %v", running, ids)
	}

	if err := st.MarkSessionTerminated(ctx, running); err != nil {
		t.Fatalf("mark terminated: %v", err)
	}
	got, _ := st.GetSession(ctx, running)
	if got.Status != "terminated" {
		t.Fatalf("expected terminated, got %s", got.Status)
	}
	if !got.TerminatedAt.Valid {
		t.Fatal("expected terminated_at set")
	}
	// idle 不受影响
	gotIdle, _ := st.GetSession(ctx, idle)
	if gotIdle.Status != "idle" {
		t.Fatalf("idle session should be untouched, got %s", gotIdle.Status)
	}
}

// TestEventSeqMonotonic 验证 AppendEvent 的 seq 单调递增、ListEvents 升序返回。
func TestEventSeqMonotonic(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	sid := makeSession(t, st)

	for i := 0; i < 5; i++ {
		if _, err := st.AppendEvent(ctx, AppendEventInput{
			SessionID: sid, Type: "user.message", Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	evs, err := st.ListEvents(ctx, sid, nil)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("expected 5 events, got %d", len(evs))
	}
	for i, ev := range evs {
		if int(ev.Seq) != i+1 {
			t.Fatalf("event %d expected seq %d, got %d", i, i+1, ev.Seq)
		}
	}
}
