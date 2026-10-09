package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
)

func (g *Guard) Login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(g, r) || r.Method != "POST" {
		httpx.Fail(w, 403, "origin", "Origin rejected")
		return
	}
	var in auth.LoginRequest
	if e := httpx.Decode(r, &in); e != nil {
		httpx.Fail(w, 400, "invalid_json", "Invalid JSON body")
		return
	}
	tok, s, e := g.Auth.Login(r.Context(), in.Email, in.Password)
	if errors.Is(e, auth.ErrRateLimited) {
		w.Header().Set("Retry-After", "60")
		httpx.Fail(w, 429, "rate_limited", "Too many login attempts")
		return
	}
	if e != nil {
		httpx.Fail(w, 401, "invalid_credentials", "Invalid credentials")
		return
	}
	if old, err := r.Cookie(g.cookie()); err == nil {
		if err := g.Auth.Logout(r.Context(), old.Value); err != nil {
			_ = g.Auth.Logout(r.Context(), tok)
			httpx.Fail(w, 500, "internal_error", "Login failed")
			return
		}
	}
	g.setCookie(w, tok, int(g.Auth.TTL.Seconds()))
	httpx.Write(w, 200, s)
}
func (g *Guard) Session(w http.ResponseWriter, r *http.Request) {
	s, ok := g.session(r)
	if !ok {
		httpx.Fail(w, 401, "unauthorized", "Unauthorized")
		return
	}
	httpx.Write(w, 200, s)
}
func (g *Guard) Logout(w http.ResponseWriter, r *http.Request) {
	s, ok := g.session(r)
	if !ok || r.Header.Get("X-CSRF-Token") != s.CSRFToken || !sameOrigin(g, r) {
		httpx.Fail(w, 403, "csrf", "CSRF token required")
		return
	}
	if c, e := r.Cookie(g.cookie()); e == nil {
		if e := g.Auth.Logout(r.Context(), c.Value); e != nil {
			httpx.Fail(w, 500, "internal_error", "Logout failed")
			return
		}
	}
	g.setCookie(w, "", -1)
	httpx.Write(w, 200, nil)
}
func (g *Guard) setCookie(w http.ResponseWriter, v string, maxAge int) {
	cookie := &http.Cookie{Name: g.cookie(), Value: v, Path: "/", HttpOnly: true, Secure: g.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
	if maxAge < 0 {
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}
func sameOrigin(g *Guard, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if g.Origin == "" {
		u, err := url.Parse(origin)
		return err == nil && u.Scheme == "http" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	}
	return strings.TrimRight(origin, "/") == strings.TrimRight(g.Origin, "/")
}
