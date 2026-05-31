package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/auth"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// MountAPIKeyRoutes 挂载 /admin/api_keys/*（ADR-0013 F-M1-014）。
func MountAPIKeyRoutes(r chi.Router, svc *auth.Service) {
	r.Route("/api_keys", func(r chi.Router) {
		r.Post("/", createAPIKey(svc))
		r.Get("/", listAPIKeys(svc))
		r.Delete("/{id}", revokeAPIKey(svc))
	})
}

func createAPIKey(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // name 可选
		row, plaintext, err := svc.IssueAPIKey(r.Context(), tenantFromCtx(r), userFromCtx(r), req.Name)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		out := apiKeyView(row)
		out["key"] = plaintext // 仅创建时返回一次明文
		writeJSON(w, 201, out)
	}
}

func listAPIKeys(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := svc.ListAPIKeys(r.Context(), tenantFromCtx(r))
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			items = append(items, apiKeyView(row))
		}
		writeJSON(w, 200, map[string]any{"data": items, "has_more": false})
	}
}

func revokeAPIKey(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := svc.RevokeAPIKey(r.Context(), id); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "api_key_revoked"})
	}
}

func apiKeyView(r *store.APIKeyRow) map[string]any {
	return map[string]any{
		"type":       "api_key",
		"id":         r.ID,
		"name":       r.Name,
		"prefix":     r.Prefix,
		"created_at": r.CreatedAt,
	}
}
