package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Sessions ─────────────────────────────────────────────────────────────

func createSession(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apitypes.CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		out, err := d.Session.Create(r.Context(), tenantFromCtx(r), req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 201, out)
	}
}

func listSessions(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := d.Session.List(r.Context(), tenantFromCtx(r), limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, apitypes.ListResponse[*apitypes.Session]{Data: items, HasMore: false})
	}
}

func getSession(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		s, err := d.Session.Get(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, s)
	}
}

func deleteSession(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Session.Delete(r.Context(), id); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "session_deleted"})
	}
}

func updateSession(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req apitypes.UpdateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		out, err := d.Session.Update(r.Context(), id, req)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func archiveSession(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Session.Archive(r.Context(), id); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		s, err := d.Session.Get(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, s)
	}
}
