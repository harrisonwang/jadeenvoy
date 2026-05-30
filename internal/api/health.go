package api

import "net/http"

// ─── Health ───────────────────────────────────────────────────────────────

func health(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"status":  "ok",
			"runtime": "go",
			"backends": map[string]string{
				"db": d.Store.Driver,
			},
			"auth": d.AuthMode,
		})
	}
}
