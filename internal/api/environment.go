package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Environments ─────────────────────────────────────────────────────────────
//
// 对齐 Anthropic spec：config.type ∈ {cloud, self_hosted}。
// self_hosted 是反向 work-queue 架构，与 JadeEnvoy「整链自托管」方向相反，明确返回 501
// （不静默 stub，见 ADR-0007 / why-jadeenvoy.md）。

func createEnvironment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apitypes.CreateEnvironmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if req.Name == "" {
			writeErr(w, 400, "invalid_request_error", "name is required")
			return
		}
		switch envConfigType(req.Config) {
		case "", "cloud":
			// ok
		case "self_hosted":
			writeErr(w, 501, "not_implemented_error",
				"self_hosted environments are not supported; JadeEnvoy runs the full agent loop locally (see ADR-0007)")
			return
		default:
			writeErr(w, 400, "invalid_request_error", "config.type must be 'cloud' or 'self_hosted'")
			return
		}
		row, err := d.Store.CreateEnvironment(r.Context(), tenantFromCtx(r), req.Name, req.Config)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 201, environmentToAPI(row))
	}
}

func listEnvironments(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := d.Store.ListEnvironments(r.Context(), tenantFromCtx(r), limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		out := make([]*apitypes.Environment, 0, len(rows))
		for _, row := range rows {
			out = append(out, environmentToAPI(row))
		}
		writeJSON(w, 200, apitypes.ListResponse[*apitypes.Environment]{Data: out, HasMore: false})
	}
}

func getEnvironment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		row, err := d.Store.GetEnvironment(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, environmentToAPI(row))
	}
}

func updateEnvironment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req apitypes.CreateEnvironmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if req.Name == "" {
			writeErr(w, 400, "invalid_request_error", "name is required")
			return
		}
		switch envConfigType(req.Config) {
		case "", "cloud":
			// ok
		case "self_hosted":
			writeErr(w, 501, "not_implemented_error",
				"self_hosted environments are not supported; JadeEnvoy runs the full agent loop locally (see ADR-0007)")
			return
		default:
			writeErr(w, 400, "invalid_request_error", "config.type must be 'cloud' or 'self_hosted'")
			return
		}
		row, err := d.Store.UpdateEnvironment(r.Context(), id, req.Name, req.Config)
		if err != nil {
			if err == store.ErrNotFound {
				writeErr(w, 404, "not_found_error", err.Error())
				return
			}
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, environmentToAPI(row))
	}
}

func archiveEnvironment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		row, err := d.Store.ArchiveEnvironment(r.Context(), id)
		if err != nil {
			if err == store.ErrNotFound {
				writeErr(w, 404, "not_found_error", err.Error())
				return
			}
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, environmentToAPI(row))
	}
}

func deleteEnvironment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Store.DeleteEnvironment(r.Context(), id); err != nil {
			if err == store.ErrNotFound {
				writeErr(w, 404, "not_found_error", err.Error())
				return
			}
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "environment_deleted"})
	}
}

func envConfigType(config json.RawMessage) string {
	if len(config) == 0 {
		return ""
	}
	var c struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(config, &c)
	return c.Type
}

func environmentToAPI(r *store.EnvironmentRow) *apitypes.Environment {
	cfg := r.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	out := &apitypes.Environment{
		Type:      "environment",
		ID:        r.ID,
		Name:      r.Name,
		Config:    cfg,
		CreatedAt: r.CreatedAt,
	}
	u := r.UpdatedAt
	out.UpdatedAt = &u
	if r.ArchivedAt.Valid {
		at := time.UnixMilli(r.ArchivedAt.Int64).UTC()
		out.ArchivedAt = &at
	}
	return out
}
