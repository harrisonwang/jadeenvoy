package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Middleware ───────────────────────────────────────────────────────────

func loggingMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		dur := time.Since(start)
		obs.Logger().Info("http",
			"method", r.Method, "path", r.URL.Path,
			"status", ww.Status(), "ms", dur.Milliseconds())

		// Prometheus metrics（用 routepattern，避免高基数）
		pattern := chi.RouteContext(r.Context()).RoutePattern()
		if pattern == "" {
			pattern = "unknown"
		}
		obs.HTTPRequests.WithLabelValues(r.Method, pattern, strconv.Itoa(ww.Status())).Inc()
		obs.HTTPLatency.WithLabelValues(r.Method, pattern).Observe(dur.Seconds())
	})
}

// ─── Auth context ─────────────────────────────────────────────────────────

type ctxKey int

const (
	ctxTenant ctxKey = iota
	ctxUser
)

func withIdentity(ctx context.Context, tenant, user string) context.Context {
	ctx = context.WithValue(ctx, ctxTenant, tenant)
	ctx = context.WithValue(ctx, ctxUser, user)
	return ctx
}

// tenantFromCtx 取请求关联的租户；未认证 / bypass 回退默认租户。
func tenantFromCtx(r *http.Request) string {
	if v, ok := r.Context().Value(ctxTenant).(string); ok && v != "" {
		return v
	}
	return "tnt-default"
}

// userFromCtx 取请求关联的用户 id（无主 API key 时为空）。
func userFromCtx(r *http.Request) string {
	if v, ok := r.Context().Value(ctxUser).(string); ok {
		return v
	}
	return ""
}

// RequireAuth 是鉴权 middleware（ADR-0013）。解析顺序：
//
//  1. x-api-key header → 程序化调用
//  2. cookie session   → 浏览器 Console
//  3. bypass/optional  → 放行为 default 租户
//  4. 否则 401
//
// 仅在 d.Auth 注入时挂载（保证旧测试与无 auth 装配路径行为不变）。
func RequireAuth(d *Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if d.Auth != nil {
				if k := r.Header.Get("x-api-key"); k != "" {
					if key, err := d.Auth.ResolveAPIKey(r.Context(), k); err == nil {
						user := ""
						if key.UserID.Valid {
							user = key.UserID.String
						}
						next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), key.TenantID, user)))
						return
					}
				}
				if c, err := r.Cookie(d.Auth.CookieName()); err == nil {
					if sess, u, err := d.Auth.ResolveSession(r.Context(), c.Value); err == nil {
						next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), sess.TenantID, u.ID)))
						return
					}
				}
			}
			// bypass / optional：未认证按默认租户放行（optional 解决 Console 混合场景）。
			if d.AuthMode == "bypass" || d.AuthMode == "optional" {
				next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), "tnt-default", bypassUserID)))
				return
			}
			writeErr(w, 401, "authentication_error", "missing or invalid credentials")
		})
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, apitypes.ErrorResponse{
		Error: apitypes.ErrorBody{Type: errType, Message: msg},
	})
}

func safeUploadPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return false
	}
	clean := strings.ReplaceAll(p, `\\`, "/")
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
