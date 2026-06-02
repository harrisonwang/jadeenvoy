package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

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
		triggerThreads := map[string]struct{}{}
		for _, raw := range req.Events {
			var typed struct {
				Type     string `json:"type"`
				ThreadID string `json:"session_thread_id"`
			}
			if err := json.Unmarshal(raw, &typed); err != nil {
				writeErr(w, 400, "invalid_request_error", "event missing type")
				return
			}
			if typed.ThreadID == "" {
				typed.ThreadID = "primary"
			}
			_, err := d.Broker.Publish(r.Context(), sessionID, typed.Type, typed.ThreadID, raw)
			if err != nil {
				writeErr(w, 500, "internal_error", err.Error())
				return
			}
			switch typed.Type {
			case "user.message", "user.custom_tool_result", "user.tool_confirmation":
				triggerThreads[typed.ThreadID] = struct{}{}
			case "user.interrupt":
				// 打断运行中的 turn（ADR-0025）。不触发新 turn。
				if d.Harness != nil {
					d.Harness.Interrupt(sessionID)
				}
			}
		}

		// 触发 turn（异步，不阻塞 client）。
		// 不能用 r.Context() —— handler return 后立刻 cancel。
		for threadID := range triggerThreads {
			threadID := threadID
			go func() {
				bg := context.Background()
				if err := d.Harness.RunThread(bg, sessionID, threadID); err != nil {
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
		threadID := r.URL.Query().Get("session_thread_id")
		if threadID == "" {
			threadID = r.URL.Query().Get("thread_id")
		}
		out := make([]json.RawMessage, 0, len(rows))
		for _, ev := range rows {
			if threadID != "" && ev.ThreadID != threadID {
				continue
			}
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

		// 客户端可带 ?since=<seq> 或 Last-Event-ID 头要求补发其后的事件。
		// 无回放会在"连接晚于终态事件"或"断线重连"时永久死锁（官方明确警告）。
		since := parseSinceSeq(r)

		isTerminal := func(t string) bool {
			return t == "session.status_idle" || t == "session.status_terminated"
		}
		writeEvent := func(row *store.EventRow) {
			payload := mergeEventPayload(row)
			_, _ = w.Write([]byte("id: " + itoa(row.Seq) + "\n"))
			_, _ = w.Write([]byte("event: " + row.Type + "\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}

		// 先订阅再回放：避免回放与订阅之间产生事件丢失的时间窗。
		ch, unsub := d.Broker.Subscribe(sessionID)
		defer unsub()

		// 回放历史（seq > since）。若历史里已含终态事件，补发后直接结束 —— 这正是
		// 修复死锁的关键：晚连接/重连的客户端也能拿到已经发生的 idle/terminated。
		history, err := d.Store.ListEvents(r.Context(), sessionID, nil)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		var maxReplayed int64 = since
		for _, ev := range history {
			if ev.Seq <= since {
				continue
			}
			writeEvent(ev)
			maxReplayed = ev.Seq
			if isTerminal(ev.Type) {
				return // 终态已补发，无需再等实时
			}
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Seq <= maxReplayed {
					continue // 去重：已在回放阶段发过
				}
				row := &store.EventRow{
					ID: ev.ID, SessionID: ev.SessionID, ThreadID: ev.ThreadID,
					Seq: ev.Seq, Type: ev.Type, Payload: ev.Payload,
				}
				writeEvent(row)
				if isTerminal(ev.Type) {
					return
				}
			}
		}
	}
}

// parseSinceSeq 从 ?since= 或 Last-Event-ID 头取断点 seq；缺省 0（全量回放）。
func parseSinceSeq(r *http.Request) int64 {
	v := r.URL.Query().Get("since")
	if v == "" {
		v = r.Header.Get("Last-Event-ID")
	}
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

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
