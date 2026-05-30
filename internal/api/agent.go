package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Agents ───────────────────────────────────────────────────────────────

func createAgent(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apitypes.CreateAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if req.Name == "" {
			writeErr(w, 400, "invalid_request_error", "name is required")
			return
		}
		if req.Model.ID == "" {
			writeErr(w, 400, "invalid_request_error", "model is required")
			return
		}
		out, err := d.Agent.Create(r.Context(), tenantFromCtx(r), req)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 201, out)
	}
}

func listAgents(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := d.Agent.List(r.Context(), tenantFromCtx(r), limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, apitypes.ListResponse[*apitypes.Agent]{Data: items, HasMore: false})
	}
}

func getAgent(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		a, err := d.Agent.Get(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, a)
	}
}

func updateAgent(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req apitypes.UpdateAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if req.Name == "" {
			writeErr(w, 400, "invalid_request_error", "name is required")
			return
		}
		if req.Model.ID == "" {
			writeErr(w, 400, "invalid_request_error", "model is required")
			return
		}
		if req.Version <= 0 {
			writeErr(w, 400, "invalid_request_error", "version is required")
			return
		}
		out, err := d.Agent.Update(r.Context(), id, req)
		if err != nil {
			if strings.Contains(err.Error(), "version conflict") {
				writeErr(w, 409, "conflict_error", err.Error())
				return
			}
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func deleteAgent(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Agent.Delete(r.Context(), id); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "agent_deleted"})
	}
}

func archiveAgent(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Agent.Archive(r.Context(), id); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		a, err := d.Agent.Get(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, a)
	}
}

func listAgentVersions(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		items, err := d.Agent.Versions(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, apitypes.ListResponse[*apitypes.Agent]{Data: items, HasMore: false})
	}
}
