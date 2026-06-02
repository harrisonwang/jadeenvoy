package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// ─── Session Resources (M2) ───────────────────────────────────────────────

func addSessionResource(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		// 校验 session 存在
		if _, err := d.Session.Get(r.Context(), sessionID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		resType, _ := req["type"].(string)
		if resType == "" {
			writeErr(w, 400, "invalid_request_error", "type is required")
			return
		}
		payload, _ := json.Marshal(req)
		rr, err := d.Store.AddSessionResource(r.Context(), store.AddSessionResourceInput{
			SessionID: sessionID,
			Type:      resType,
			Payload:   payload,
		})
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 201, map[string]any{
			"type":       "session_resource",
			"id":         rr.ID,
			"session_id": sessionID,
			"resource":   json.RawMessage(rr.Payload),
			"created_at": rr.CreatedAt,
		})
	}
}

func listSessionResources(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		if _, err := d.Session.Get(r.Context(), sessionID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		rows, err := d.Store.ListSessionResources(r.Context(), sessionID)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, rr := range rows {
			out = append(out, sessionResourceToAPI(rr))
		}
		writeJSON(w, 200, map[string]any{"data": out, "has_more": false})
	}
}

func getSessionResource(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		resID := chi.URLParam(r, "resId")
		if _, err := d.Session.Get(r.Context(), sessionID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		rr, err := d.Store.GetSessionResource(r.Context(), resID)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		if rr.SessionID != sessionID {
			writeErr(w, 404, "not_found_error", "session resource not found")
			return
		}
		writeJSON(w, 200, sessionResourceToAPI(rr))
	}
}

func updateSessionResource(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		resID := chi.URLParam(r, "resId")
		if _, err := d.Session.Get(r.Context(), sessionID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		existing, err := d.Store.GetSessionResource(r.Context(), resID)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		if existing.SessionID != sessionID {
			writeErr(w, 404, "not_found_error", "session resource not found")
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		resType, _ := req["type"].(string)
		if resType == "" {
			writeErr(w, 400, "invalid_request_error", "type is required")
			return
		}
		payload, _ := json.Marshal(req)
		rr, err := d.Store.UpdateSessionResource(r.Context(), store.UpdateSessionResourceInput{
			ID:      resID,
			Type:    resType,
			Payload: payload,
		})
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, sessionResourceToAPI(rr))
	}
}

func deleteSessionResource(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := chi.URLParam(r, "id")
		resID := chi.URLParam(r, "resId")
		if _, err := d.Session.Get(r.Context(), sessionID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		rr, err := d.Store.GetSessionResource(r.Context(), resID)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		if rr.SessionID != sessionID {
			writeErr(w, 404, "not_found_error", "session resource not found")
			return
		}
		if err := d.Store.DeleteSessionResource(r.Context(), resID); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": resID, "type": "session_resource_deleted"})
	}
}

func sessionResourceToAPI(rr *store.SessionResourceRow) map[string]any {
	return map[string]any{
		"type":       "session_resource",
		"id":         rr.ID,
		"session_id": rr.SessionID,
		"resource":   json.RawMessage(rr.Payload),
		"created_at": rr.CreatedAt,
	}
}
