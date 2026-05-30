package api

import (
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

func tenantFromCtx(r *http.Request) string {
	// V1 bypass: 默认租户
	return "tnt-default"
}
