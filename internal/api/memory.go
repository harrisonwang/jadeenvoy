package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/memory"
)

// ─── Memory Stores routes ────────────────────────────────────────────────

func MountMemoryRoutes(r chi.Router, svc *memory.Service) {
	r.Route("/memory_stores", func(r chi.Router) {
		r.Post("/", createMemoryStore(svc))
		r.Get("/", listMemoryStores(svc))
		r.Get("/{id}", getMemoryStore(svc))
		r.Post("/{id}", updateMemoryStore(svc))
		r.Delete("/{id}", deleteMemoryStore(svc))
		r.Post("/{id}/archive", archiveMemoryStore(svc))

		r.Post("/{id}/memories", upsertMemory(svc))
		r.Get("/{id}/memories", listMemories(svc))
		r.Get("/{id}/memories/{mid}", getMemory(svc))
		r.Patch("/{id}/memories/{mid}", updateMemory(svc))
		r.Delete("/{id}/memories/{mid}", deleteMemory(svc))

		r.Get("/{id}/memory_versions", listMemoryVersions(svc))
		r.Get("/{id}/memory_versions/{vid}", getMemoryVersion(svc))
		r.Post("/{id}/memory_versions/{vid}/redact", redactMemoryVersion(svc))
	})
}

func createMemoryStore(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req memory.CreateStoreRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		out, err := svc.CreateStore(r.Context(), tenantFromCtx(r), req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 201, out)
	}
}

func listMemoryStores(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := svc.ListStores(r.Context(), tenantFromCtx(r), limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"data": items, "has_more": false})
	}
}

func getMemoryStore(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.GetStore(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func deleteMemoryStore(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := svc.DeleteStore(r.Context(), id); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "memory_store_deleted"})
	}
}

func updateMemoryStore(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req memory.CreateStoreRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		out, err := svc.UpdateStore(r.Context(), chi.URLParam(r, "id"), req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func archiveMemoryStore(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.ArchiveStore(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func upsertMemory(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storeID := chi.URLParam(r, "id")
		var req memory.UpsertMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		out, err := svc.UpsertMemory(r.Context(), tenantFromCtx(r), storeID, req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 201, out)
	}
}

func listMemories(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storeID := chi.URLParam(r, "id")
		prefix := r.URL.Query().Get("path_prefix")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := svc.ListMemories(r.Context(), storeID, prefix, limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"data": items, "has_more": false})
	}
}

func getMemory(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.GetMemory(r.Context(), chi.URLParam(r, "mid"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func deleteMemory(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mid := chi.URLParam(r, "mid")
		if err := svc.DeleteMemory(r.Context(), mid); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": mid, "type": "memory_deleted"})
	}
}

func updateMemory(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req memory.UpdateMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		out, err := svc.UpdateMemory(r.Context(), chi.URLParam(r, "mid"), req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func listMemoryVersions(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storeID := chi.URLParam(r, "id")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := svc.ListMemoryVersions(r.Context(), storeID, r.URL.Query().Get("memory_id"), limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"data": items, "has_more": false})
	}
}

func getMemoryVersion(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storeID := chi.URLParam(r, "id")
		out, err := svc.GetMemoryVersion(r.Context(), chi.URLParam(r, "vid"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		if out.MemoryStoreID != storeID {
			writeErr(w, 404, "not_found_error", "memory version not found")
			return
		}
		writeJSON(w, 200, out)
	}
}

func redactMemoryVersion(svc *memory.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storeID := chi.URLParam(r, "id")
		out, err := svc.RedactMemoryVersion(r.Context(), chi.URLParam(r, "vid"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		if out.MemoryStoreID != storeID {
			writeErr(w, 404, "not_found_error", "memory version not found")
			return
		}
		writeJSON(w, 200, out)
	}
}
