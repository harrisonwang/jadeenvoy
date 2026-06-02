package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/vault"
	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

// MountVaultRoutes 挂载 /v1/vaults/*（ADR-0015）。
func MountVaultRoutes(r chi.Router, svc *vault.Service) {
	r.Route("/vaults", func(r chi.Router) {
		r.Post("/", createVault(svc))
		r.Get("/", listVaults(svc))
		r.Get("/{id}", getVault(svc))
		r.Post("/{id}", updateVault(svc))
		r.Delete("/{id}", deleteVault(svc))
		r.Post("/{id}/archive", archiveVault(svc))
		r.Post("/{id}/credentials", addCredential(svc))
		r.Get("/{id}/credentials", listCredentials(svc))
		r.Get("/{id}/credentials/{credId}", getCredential(svc))
		r.Post("/{id}/credentials/{credId}", updateCredential(svc))
		r.Delete("/{id}/credentials/{credId}", deleteCredential(svc))
		r.Post("/{id}/credentials/{credId}/archive", archiveCredential(svc))
		r.Post("/{id}/credentials/{credId}/mcp_oauth_validate", validateOAuthCredential(svc))
	})
}

func createVault(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apitypes.CreateVaultRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		if req.DisplayName == "" {
			writeErr(w, 400, "invalid_request_error", "display_name is required")
			return
		}
		out, err := svc.CreateVault(r.Context(), tenantFromCtx(r), req)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 201, out)
	}
}

func listVaults(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := svc.ListVaults(r.Context(), tenantFromCtx(r), limit)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, apitypes.ListResponse[*apitypes.Vault]{Data: items, HasMore: false})
	}
}

func getVault(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := svc.GetVault(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, v)
	}
}

func archiveVault(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantFromCtx(r)
		id := chi.URLParam(r, "id")
		if err := svc.ArchiveVault(r.Context(), tenant, id); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		v, err := svc.GetVault(r.Context(), tenant, id)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, v)
	}
}

func updateVault(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apitypes.CreateVaultRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		out, err := svc.UpdateVault(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), req)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, 404, "not_found_error", err.Error())
				return
			}
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func deleteVault(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := svc.DeleteVault(r.Context(), tenantFromCtx(r), id); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "type": "vault_deleted"})
	}
}

func addCredential(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vaultID := chi.URLParam(r, "id")
		var req apitypes.CreateCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		out, err := svc.AddCredential(r.Context(), vaultID, tenantFromCtx(r), req)
		if err != nil {
			switch {
			case errors.Is(err, vault.ErrUnsupportedAuthType):
				writeErr(w, 400, "invalid_request_error", "unsupported credential auth type")
			case errors.Is(err, store.ErrConflict):
				writeErr(w, 409, "conflict_error", "a credential for this host already exists in the vault; archive it first")
			case errors.Is(err, store.ErrNotFound):
				writeErr(w, 404, "not_found_error", "vault not found")
			default:
				writeErr(w, 400, "invalid_request_error", err.Error())
			}
			return
		}
		writeJSON(w, 201, out)
	}
}

func listCredentials(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := svc.ListCredentials(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, 404, "not_found_error", "vault not found")
			} else {
				writeErr(w, 500, "internal_error", err.Error())
			}
			return
		}
		writeJSON(w, 200, apitypes.ListResponse[*apitypes.Credential]{Data: items, HasMore: false})
	}
}

func getCredential(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.GetCredential(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), chi.URLParam(r, "credId"))
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func updateCredential(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apitypes.CreateCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_request_error", "invalid json: "+err.Error())
			return
		}
		out, err := svc.UpdateCredential(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), chi.URLParam(r, "credId"), req)
		if err != nil {
			switch {
			case errors.Is(err, vault.ErrUnsupportedAuthType):
				writeErr(w, 400, "invalid_request_error", "unsupported credential auth type")
			case errors.Is(err, store.ErrConflict):
				writeErr(w, 409, "conflict_error", "a credential for this host already exists in the vault; archive it first")
			case errors.Is(err, store.ErrNotFound):
				writeErr(w, 404, "not_found_error", "credential not found")
			default:
				writeErr(w, 400, "invalid_request_error", err.Error())
			}
			return
		}
		writeJSON(w, 200, out)
	}
}

func archiveCredential(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credID := chi.URLParam(r, "credId")
		if err := svc.ArchiveCredential(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), credID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		out, err := svc.GetCredential(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), credID)
		if err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, out)
	}
}

func deleteCredential(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credID := chi.URLParam(r, "credId")
		if err := svc.DeleteCredential(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), credID); err != nil {
			writeErr(w, 404, "not_found_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": credID, "type": "credential_deleted"})
	}
}

func validateOAuthCredential(svc *vault.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := svc.ValidateOAuthCredential(r.Context(), tenantFromCtx(r), chi.URLParam(r, "id"), chi.URLParam(r, "credId")); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, 404, "not_found_error", "credential not found")
				return
			}
			writeErr(w, 400, "invalid_request_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	}
}
