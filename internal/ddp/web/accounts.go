package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
)

type inviteRequest struct {
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

type resetRequest struct {
	Email string `json:"email"`
}

type passwordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (g *Guard) Invite(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !sameOrigin(g, r) {
			httpx.Fail(w, http.StatusForbidden, "origin", "Origin rejected")
			return
		}
		var in inviteRequest
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
			return
		}
		sess, _ := CurrentSession(r.Context())
		inv, err := g.Auth.Invite(r.Context(), cfg, in.Email, in.Roles, sess.User.ID)
		if errors.Is(err, auth.ErrInvalidAccount) {
			httpx.Fail(w, http.StatusBadRequest, "invalid_invite", "Invalid invitation")
			return
		}
		if errors.Is(err, audit.ErrRefused) {
			httpx.Fail(w, http.StatusConflict, "refused", "Operation refused")
			return
		}
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "internal_error", "Invitation failed")
			return
		}
		httpx.Write(w, http.StatusCreated, inv)
	}
}

func (g *Guard) RequestReset(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !sameOrigin(g, r) {
			httpx.Fail(w, http.StatusForbidden, "origin", "Origin rejected")
			return
		}
		var in resetRequest
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
			return
		}
		started := time.Now()
		err := g.Auth.RequestReset(r.Context(), cfg, in.Email)
		if !waitMinimum(r.Context(), started) {
			return
		}
		if err != nil && !errors.Is(err, auth.ErrRateLimited) {
			httpx.Fail(w, http.StatusInternalServerError, "internal_error", "Reset request failed")
			return
		}
		httpx.Write(w, http.StatusAccepted, map[string]bool{"accepted": true})
	}
}

func (g *Guard) SetPassword(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !sameOrigin(g, r) {
			httpx.Fail(w, http.StatusForbidden, "origin", "Origin rejected")
			return
		}
		var in passwordRequest
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
			return
		}
		started := time.Now()
		if n := len(in.Password); n < 8 || n > 128 {
			if !waitMinimum(r.Context(), started) {
				return
			}
			httpx.Fail(w, http.StatusBadRequest, "invalid_password", "Invalid password")
			return
		}
		err := g.Auth.SetPassword(r.Context(), cfg, in.Token, in.Password)
		if !waitMinimum(r.Context(), started) {
			return
		}
		if errors.Is(err, auth.ErrRateLimited) {
			w.Header().Set("Retry-After", "60")
			httpx.Fail(w, http.StatusTooManyRequests, "rate_limited", "Too many authentication attempts")
			return
		}
		if errors.Is(err, auth.ErrInvalidLink) {
			httpx.Fail(w, http.StatusBadRequest, "invalid_link", "Invalid reset link")
			return
		}
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "internal_error", "Password change failed")
			return
		}
		g.setCookie(w, "", -1)
		httpx.Write(w, http.StatusOK, map[string]bool{"changed": true})
	}
}

func waitMinimum(ctx context.Context, started time.Time) bool {
	remaining := 250*time.Millisecond - time.Since(started)
	if remaining <= 0 {
		return true
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
