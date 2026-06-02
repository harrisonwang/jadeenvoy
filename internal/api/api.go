// Package api 构建 chi router 并挂载所有 handlers。
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/harrisonwang/jadeenvoy/internal/agent"
	"github.com/harrisonwang/jadeenvoy/internal/auth"
	"github.com/harrisonwang/jadeenvoy/internal/event"
	"github.com/harrisonwang/jadeenvoy/internal/harness"
	"github.com/harrisonwang/jadeenvoy/internal/memory"
	"github.com/harrisonwang/jadeenvoy/internal/session"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/vault"
	"github.com/harrisonwang/jadeenvoy/internal/webhook"
)

type Deps struct {
	Store    *store.Store
	Broker   *event.Broker
	Agent    *agent.Service
	Session  *session.Service
	Memory   *memory.Service
	Webhook  *webhook.Service
	Vault    *vault.Service
	Auth     *auth.Service
	Harness  *harness.Harness
	AuthMode string

	LLMProvider       string
	DefaultAgentModel string
}

// NewRouter 构建 chi router。
func corsMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Echo the concrete origin instead of "*" so browser cookie auth can work.
			// Local/self-hosted deployments may put the console on a different port.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, anthropic-beta, last-event-id")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewRouter 构建 chi router。
func NewRouter(d *Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(corsMW)
	r.Use(loggingMW)

	r.Get("/health", health(d))
	r.Handle("/metrics", promhttp.Handler())
	MountAuthRoutes(r, d)
	r.Route("/v1", func(r chi.Router) {
		r.Use(RequireAuth(d))
		r.Route("/agents", func(r chi.Router) {
			r.Post("/", createAgent(d))
			r.Get("/", listAgents(d))
			r.Get("/{id}", getAgent(d))
			r.Post("/{id}", updateAgent(d))
			r.Delete("/{id}", deleteAgent(d))
			r.Post("/{id}/archive", archiveAgent(d))
			r.Get("/{id}/versions", listAgentVersions(d))
		})
		r.Route("/environments", func(r chi.Router) {
			r.Post("/", createEnvironment(d))
			r.Get("/", listEnvironments(d))
			r.Get("/{id}", getEnvironment(d))
			r.Post("/{id}", updateEnvironment(d))
			r.Delete("/{id}", deleteEnvironment(d))
			r.Post("/{id}/archive", archiveEnvironment(d))
		})
		r.Route("/sessions", func(r chi.Router) {
			r.Post("/", createSession(d))
			r.Get("/", listSessions(d))
			r.Get("/{id}", getSession(d))
			r.Post("/{id}", updateSession(d))
			r.Delete("/{id}", deleteSession(d))
			r.Post("/{id}/archive", archiveSession(d))
			r.Post("/{id}/events", postEvents(d))
			r.Get("/{id}/events", listEvents(d))
			r.Get("/{id}/events/stream", streamEvents(d))
		})
		if d.Memory != nil {
			MountMemoryRoutes(r, d.Memory)
		}
		// Files API (M2)
		r.Route("/files", func(r chi.Router) {
			r.Post("/", createFile(d))
			r.Get("/", listFiles(d))
			r.Get("/{id}", getFile(d))
			r.Get("/{id}/content", getFileContent(d))
			r.Delete("/{id}", deleteFile(d))
		})
		// Skills API (M2)
		r.Route("/skills", func(r chi.Router) {
			r.Post("/", createSkill(d))
			r.Get("/", listSkills(d))
			r.Get("/{id}", getSkill(d))
			r.Delete("/{id}", deleteSkill(d))
		})
		// Session resources (M2)
		r.Route("/sessions/{id}/resources", func(r chi.Router) {
			r.Get("/", listSessionResources(d))
			r.Post("/", addSessionResource(d))
			r.Get("/{resId}", getSessionResource(d))
			r.Post("/{resId}", updateSessionResource(d))
			r.Delete("/{resId}", deleteSessionResource(d))
		})
		// Vaults (M1/ADR-0015)
		if d.Vault != nil {
			MountVaultRoutes(r, d.Vault)
		}
	})
	if d.Webhook != nil || d.Auth != nil || d.Store != nil {
		r.Route("/admin", func(r chi.Router) {
			r.Use(RequireAuth(d))
			if d.Store != nil {
				r.Get("/dashboard", getDashboard(d))
			}
			if d.Webhook != nil {
				MountWebhookRoutes(r, d.Webhook)
			}
			if d.Auth != nil {
				MountAPIKeyRoutes(r, d.Auth)
			}
		})
	}
	return r
}
