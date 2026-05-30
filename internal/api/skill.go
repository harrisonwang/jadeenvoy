package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// ─── Skills (M2) ─────────────────────────────────────────────────────────

func createSkill(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		var files []store.SkillFileEntry

		if strings.HasPrefix(ct, "multipart/form-data") {
			// Multipart zip 上传
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				writeErr(w, 400, "invalid_request_error", "multipart parse: "+err.Error())
				return
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				writeErr(w, 400, "invalid_request_error", "file field required")
				return
			}
			defer file.Close()
			zipData, err := io.ReadAll(file)
			if err != nil {
				writeErr(w, 400, "invalid_request_error", "read zip: "+err.Error())
				return
			}
			zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
			if err != nil {
				writeErr(w, 400, "invalid_request_error", "invalid zip: "+err.Error())
				return
			}
			// 从 FormField 取 name
			skillName := r.FormValue("name")
			desc := r.FormValue("description")
			for _, zf := range zr.File {
				if zf.FileInfo().IsDir() {
					continue
				}
				if !safeUploadPath(zf.Name) {
					writeErr(w, 400, "invalid_request_error", "unsafe skill file path: "+zf.Name)
					return
				}
				rc, err := zf.Open()
				if err != nil {
					continue
				}
				content, _ := io.ReadAll(rc)
				rc.Close()
				files = append(files, store.SkillFileEntry{
					Path:    zf.Name,
					Content: string(content),
				})
			}
			if skillName == "" {
				writeErr(w, 400, "invalid_request_error", "name is required")
				return
			}
			row, err := d.Store.CreateSkill(r.Context(), store.CreateSkillInput{
				TenantID:    tenantFromCtx(r),
				Name:        skillName,
				Description: desc,
				Files:       files,
			})
			if err != nil {
				writeErr(w, 500, "internal_error", err.Error())
				return
			}
			writeJSON(w, 201, skillRowToAPI(row))
			return
		}

		// JSON body
		var req apitypes.CreateSkillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if req.Name == "" {
			writeErr(w, 400, "invalid_request_error", "name is required")
			return
		}
		for _, f := range req.Files {
			if !safeUploadPath(f.Path) {
				writeErr(w, 400, "invalid_request_error", "unsafe skill file path: "+f.Path)
				return
			}
			files = append(files, store.SkillFileEntry{Path: f.Path, Content: f.Content})
		}

		row, err := d.Store.CreateSkill(r.Context(), store.CreateSkillInput{
			TenantID:    tenantFromCtx(r),
			Name:        req.Name,
			Description: req.Description,
			Files:       files,
		})
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 201, skillRowToAPI(row))
	}
}

func listSkills(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := d.Store.ListSkills(r.Context(), tenantFromCtx(r))
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		skills := make([]apitypes.Skill, 0, len(rows))
		for _, row := range rows {
			skills = append(skills, skillRowToAPI(row))
		}
		writeJSON(w, 200, map[string]any{"data": skills, "has_more": false})
	}
}

func getSkill(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		row, err := d.Store.GetSkill(r.Context(), id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, skillRowToAPI(row))
	}
}

func deleteSkill(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := d.Store.DeleteSkill(r.Context(), id); err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "skill_deleted"})
	}
}

func skillRowToAPI(r *store.SkillRow) apitypes.Skill {
	ua := r.UpdatedAt
	s := apitypes.Skill{
		Type:        "skill",
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   &ua,
	}
	var files []apitypes.SkillFile
	_ = json.Unmarshal(r.FilesJSON, &files)
	s.Files = files
	return s
}
