package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/auth"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

const bypassUserID = "usr-default"
const bypassSessionID = "sess-auth-bypass"
const sessionCookieName = "jadeenvoy_session"

// MountAuthRoutes mounts browser auth routes in every auth mode (ADR-0013 /
// oma-gaps 第 4 条). In bypass mode they return a virtual default user so the
// Console can enter the app without a login flow; in required/optional modes
// they perform real cookie-session auth backed by d.Auth.
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
		if d.Auth != nil {
			if c, err := r.Cookie(d.Auth.CookieName()); err == nil {
				if sess, u, err := d.Auth.ResolveSession(r.Context(), c.Value); err == nil {
					writeJSON(w, 200, userAuthPayload(u, sess.ID, time.UnixMilli(sess.ExpiresAt).UTC()))
					return
				}
			}
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
		if d.Auth == nil {
			writeErr(w, 501, "not_implemented_error", "password auth is not configured")
			return
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if _, err := d.Auth.Signup(r.Context(), req.Email, req.Password, req.Name); err != nil {
			if errors.Is(err, auth.ErrConflict) {
				writeErr(w, 409, "conflict_error", "an account with this email already exists")
				return
			}
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		// 注册成功后自动登录，发 cookie
		issueLogin(d, w, r, req.Email, req.Password)
	}
}

func authLogin(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AuthMode == "bypass" {
			setBypassCookie(w)
			writeJSON(w, 200, bypassAuthPayload())
			return
		}
		if d.Auth == nil {
			writeErr(w, 501, "not_implemented_error", "password auth is not configured")
			return
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		issueLogin(d, w, r, req.Email, req.Password)
	}
}

func authLogout(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Auth != nil {
			if c, err := r.Cookie(d.Auth.CookieName()); err == nil {
				_ = d.Auth.Logout(r.Context(), c.Value)
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, 200, map[string]any{"success": true})
	}
}

// issueLogin 执行真实登录并下发 session cookie。
func issueLogin(d *Deps, w http.ResponseWriter, r *http.Request, email, password string) {
	cookie, u, err := d.Auth.Login(r.Context(), email, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeErr(w, 401, "authentication_error", "invalid email or password")
			return
		}
		writeErr(w, 500, "internal_error", err.Error())
		return
	}
	expires := time.Now().UTC().Add(d.Auth.MaxAge())
	http.SetCookie(w, &http.Cookie{
		Name:     d.Auth.CookieName(),
		Value:    cookie,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, 200, userAuthPayload(u, "", expires))
}

// authPayload 构造 better-auth 兼容的 user/session 响应（camelCase + snake_case 双键），
// 供真实登录与 bypass 虚拟用户共用，避免两份 map 字面量漂移。
func authPayload(userID, name, email, sessionID string, createdAt, updatedAt, expires time.Time) map[string]any {
	return map[string]any{
		"user": map[string]any{
			"id":             userID,
			"name":           name,
			"email":          email,
			"emailVerified":  true,
			"email_verified": true,
			"image":          nil,
			"createdAt":      createdAt,
			"updatedAt":      updatedAt,
		},
		"session": map[string]any{
			"id":        sessionID,
			"userId":    userID,
			"user_id":   userID,
			"expiresAt": expires,
			"createdAt": createdAt,
			"updatedAt": updatedAt,
		},
	}
}

func userAuthPayload(u *store.UserRow, sessionID string, expires time.Time) map[string]any {
	return authPayload(u.ID, u.Name, u.Email, sessionID, u.CreatedAt, u.UpdatedAt, expires)
}

func bypassAuthPayload() map[string]any {
	now := time.Now().UTC()
	return authPayload(bypassUserID, "Default User", "default@jadeenvoy.local", bypassSessionID, now, now, now.Add(24*time.Hour))
}

func setBypassCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    bypassSessionID,
		Path:     "/",
		Expires:  time.Now().UTC().Add(24 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
