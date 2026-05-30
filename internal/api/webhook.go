package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/webhook"
)

// MountWebhookRoutes 挂载 /admin/webhooks/*。
func MountWebhookRoutes(r chi.Router, svc *webhook.Service) {
	r.Route("/webhooks", func(r chi.Router) {
		r.Post("/", createWebhookEndpoint(svc))
		r.Get("/", listWebhookEndpoints(svc))
		r.Delete("/{id}", deleteWebhookEndpoint(svc))
	})
}

func createWebhookEndpoint(svc *webhook.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req webhook.CreateEndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		out, err := svc.CreateEndpoint(r.Context(), tenantFromCtx(r), req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 201, out)
	}
}

func listWebhookEndpoints(svc *webhook.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := svc.ListEndpoints(r.Context(), tenantFromCtx(r))
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"data": items, "has_more": false})
	}
}

func deleteWebhookEndpoint(svc *webhook.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := svc.DeleteEndpoint(r.Context(), id); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "webhook_endpoint_deleted"})
	}
}
