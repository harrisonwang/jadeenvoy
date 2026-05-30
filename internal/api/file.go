package api

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Files (M2) ──────────────────────────────────────────────────────────

func createFile(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeErr(w, 400, "invalid_request_error", "multipart parse: "+err.Error())
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeErr(w, 400, "invalid_request_error", "file field required")
			return
		}
		defer file.Close()
		blob, err := io.ReadAll(file)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", "read file: "+err.Error())
			return
		}
		ct := header.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		row, err := d.Store.CreateFile(r.Context(), store.CreateFileInput{
			TenantID:    tenantFromCtx(r),
			Filename:    header.Filename,
			ContentType: ct,
			Blob:        blob,
		})
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		updatedAt := row.UpdatedAt
		writeJSON(w, 201, apitypes.File{
			Type:        "file",
			ID:          row.ID,
			Filename:    row.Filename,
			ContentType: row.ContentType,
			Size:        row.Size,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   &updatedAt,
		})
	}
}

func listFiles(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListFiles(r.Context(), tenantFromCtx(r))
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		files := make([]apitypes.File, 0, len(rows))
		for _, row := range rows {
			ua := row.UpdatedAt
			files = append(files, apitypes.File{
				Type:        "file",
				ID:          row.ID,
				Filename:    row.Filename,
				ContentType: row.ContentType,
				Size:        row.Size,
				CreatedAt:   row.CreatedAt,
				UpdatedAt:   &ua,
			})
		}
		writeJSON(w, 200, map[string]any{"data": files, "has_more": false})
	}
}

func getFile(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		row, err := d.Store.GetFile(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		ua := row.UpdatedAt
		writeJSON(w, 200, apitypes.File{
			Type:        "file",
			ID:          row.ID,
			Filename:    row.Filename,
			ContentType: row.ContentType,
			Size:        row.Size,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   &ua,
		})
	}
}

func getFileContent(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		row, err := d.Store.GetFile(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", row.ContentType)
		w.Header().Set("Content-Disposition", "attachment; filename=\""+row.Filename+"\"")
		w.WriteHeader(200)
		_, _ = w.Write(row.Blob)
	}
}

func deleteFile(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Store.DeleteFile(r.Context(), id); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "file_deleted"})
	}
}
