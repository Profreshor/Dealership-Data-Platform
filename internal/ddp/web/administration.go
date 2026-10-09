package web

import (
	"errors"
	"net/http"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
)

func (g *Guard) AccountDirectory(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		directory, err := g.Auth.Directory(r.Context(), cfg)
		if administrationError(w, err) {
			return
		}
		httpx.Write(w, http.StatusOK, directory)
	}
}

func (g *Guard) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Roles    []string `json:"roles"`
		Disabled *bool    `json:"disabled"`
	}
	if err := httpx.Decode(r, &in); err != nil || in.Roles == nil || in.Disabled == nil {
		httpx.Fail(w, http.StatusBadRequest, "invalid_json", "Roles and disabled status are required")
		return
	}
	session, _ := CurrentSession(r.Context())
	if administrationError(w, g.Auth.UpdateAccount(r.Context(), r.PathValue("id"), in.Roles, *in.Disabled, session.User.ID)) {
		return
	}
	httpx.Write(w, http.StatusOK, map[string]bool{"changed": true})
}

func (g *Guard) SaveRole(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		if err := httpx.Decode(r, &in); err != nil || in.Permissions == nil {
			httpx.Fail(w, http.StatusBadRequest, "invalid_json", "Name and permissions are required")
			return
		}
		session, _ := CurrentSession(r.Context())
		if administrationError(w, g.Auth.SaveRole(r.Context(), cfg, r.PathValue("id"), in.Name, in.Permissions, session.User.ID)) {
			return
		}
		httpx.Write(w, http.StatusOK, map[string]bool{"changed": true})
	}
}

func administrationError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, auth.ErrInvalidAccount):
		httpx.Fail(w, http.StatusBadRequest, "invalid_account", "Invalid account or role change")
	case errors.Is(err, audit.ErrRefused):
		httpx.Fail(w, http.StatusConflict, "refused", "Operation refused")
	default:
		httpx.Fail(w, http.StatusInternalServerError, "internal_error", "Account operation failed")
	}
	return true
}
