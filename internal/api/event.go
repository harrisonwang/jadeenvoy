package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Events ───────────────────────────────────────────────────────────────

func postEvents(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		var req apitypes.SendEventsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		// 校验 session 存在
		if _, err := d.Session.Get(r.Context(), sessionID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}

		// 持久化所有 user 事件
		triggerTurn := false
		for _, raw := range req.Events {
			var typed struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &typed); err != nil {
				writeErr(w, 400, "invalid_request_error", "event missing type")
				return
			}
			_, err := d.Broker.Publish(r.Context(), sessionID, typed.Type, "primary", raw)
			if err != nil {
				writeErr(w, 500, "internal_error", err.Error())
				return
			}
			if typed.Type == "user.message" || typed.Type == "user.custom_tool_result" {
				triggerTurn = true
			}
		}

		// 触发 turn（异步，不阻塞 client）。
		// 不能用 r.Context() —— handler return 后立刻 cancel。
		if triggerTurn {
			go func() {
				bg := context.Background()
				if err := d.Harness.RunTurn(bg, sessionID); err != nil {
					obs.Logger().Error("harness.turn.error", "session_id", sessionID, "err", err.Error())
				}
			}()
		}

		writeJSON(w, 202, map[string]string{"status": "accepted"})
	}
}

func listEvents(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		typesQ := r.URL.Query()["types[]"]
		if len(typesQ) == 0 {
			typesQ = r.URL.Query()["types"]
		}
		rows, err := d.Store.ListEvents(r.Context(), sessionID, typesQ)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		out := make([]json.RawMessage, 0, len(rows))
		for _, ev := range rows {
			merged := mergeEventPayload(ev)
			out = append(out, merged)
		}
		writeJSON(w, 200, apitypes.EventListResponse{Data: out, HasMore: false})
	}
}

func streamEvents(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, 500, "internal_error", "streaming not supported")
			return
		}
		ch, unsub := d.Broker.Subscribe(sessionID)
		defer unsub()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				row := &store.EventRow{
					ID: ev.ID, SessionID: ev.SessionID, ThreadID: ev.ThreadID,
					Seq: ev.Seq, Type: ev.Type, Payload: ev.Payload,
				}
				payload := mergeEventPayload(row)
				_, _ = w.Write([]byte("event: " + ev.Type + "\n"))
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(payload)
				_, _ = w.Write([]byte("\n\n"))
				flusher.Flush()
				if ev.Type == "session.status_idle" || ev.Type == "session.status_terminated" {
					return
				}
			}
		}
	}
}

// mergeEventPayload 把 store row 的 payload + 顶层字段（id/seq/session_id/processed_at）合并成一个 JSON。
func mergeEventPayload(ev *store.EventRow) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(ev.Payload, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	m["id"] = ev.ID
	m["session_id"] = ev.SessionID
	m["session_thread_id"] = ev.ThreadID
	m["seq"] = ev.Seq
	if ev.ProcessedAt.Valid {
		m["processed_at"] = ev.ProcessedAt.Int64
	}
	out, _ := json.Marshal(m)
	return out
}
