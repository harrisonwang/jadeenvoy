package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

const bypassUserID = "usr-default"
const bypassSessionID = "sess-auth-bypass"

// MountAuthRoutes mounts browser auth routes in every auth mode. In bypass mode
// they return a virtual default user so Console can enter the app without a
// login flow. Required/optional modes are placeholders until password auth is
// implemented.
func MountAuthRoutes(r chi.Router, d *Deps) {
	r.Route("/api/auth", func(r chi.Router) {
		r.Get("/session", authSession(d))
		r.Post("/signup", authSignup(d))
		r.Post("/login", authLogin(d))
		r.Post("/logout", authLogout(d))
	})
}

func authSession(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AuthMode == "bypass" {
			writeJSON(w, 200, bypassAuthPayload())
			return
		}
		writeErr(w, 401, "authentication_error", "not authenticated")
	}
}

func authSignup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AuthMode == "bypass" {
			setBypassCookie(w)
			writeJSON(w, 200, bypassAuthPayload())
			return
		}
		writeErr(w, 501, "not_implemented_error", "password signup is not implemented yet")
	}
}

func authLogin(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AuthMode == "bypass" {
			setBypassCookie(w)
			writeJSON(w, 200, bypassAuthPayload())
			return
		}
		writeErr(w, 501, "not_implemented_error", "password login is not implemented yet")
	}
}

func authLogout(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "jadeenvoy_session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, 200, map[string]any{"success": true})
	}
}

func bypassAuthPayload() map[string]any {
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	return map[string]any{
		"user": map[string]any{
			"id":             bypassUserID,
			"name":           "Default User",
			"email":          "default@jadeenvoy.local",
			"emailVerified":  true,
			"email_verified": true,
			"image":          nil,
			"createdAt":      now,
			"updatedAt":      now,
		},
		"session": map[string]any{
			"id":        bypassSessionID,
			"userId":    bypassUserID,
			"user_id":   bypassUserID,
			"expiresAt": expires,
			"createdAt": now,
			"updatedAt": now,
		},
	}
}

func setBypassCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "jadeenvoy_session",
		Value:    bypassSessionID,
		Path:     "/",
		Expires:  time.Now().UTC().Add(24 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
