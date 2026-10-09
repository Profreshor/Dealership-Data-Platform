// Package serving owns HTTP startup and database readiness.
package serving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/frontend"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/endpoints"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/metrics"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Handler(pool *pgxpool.Pool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		conn, err := pool.Acquire(ctx)
		ready := err == nil
		if ready {
			defer conn.Release()
			entries, err := migrate.Status(ctx, conn.Conn())
			ready = err == nil
			for _, entry := range entries {
				ready = ready && entry.Applied
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": ready})
	})
	return mux
}

func portalRegistry(pool *pgxpool.Pool, cfg *config.Config, register func(*web.Registry)) (*web.Registry, *web.Guard, error) {
	publicURL, err := url.Parse(cfg.Serving.PublicURL)
	if err != nil || publicURL.Host == "" || publicURL.User != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") {
		return nil, nil, fmt.Errorf("invalid serving.public_url")
	}
	ttl, err := time.ParseDuration(cfg.Serving.SessionTTL)
	if err != nil || ttl <= 0 {
		return nil, nil, fmt.Errorf("invalid serving.session_ttl")
	}
	guard := &web.Guard{Auth: auth.New(pool, ttl), Origin: publicURL.Scheme + "://" + publicURL.Host, Secure: publicURL.Scheme == "https"}
	reg := web.NewRegistry()
	reg.Pool = pool
	health := Handler(pool)
	reg.Handle("GET /healthz", health, web.Public())
	reg.Handle("GET /readyz", health, web.Public())
	reg.Handle("POST /api/auth/login", http.HandlerFunc(guard.Login), web.Public())
	reg.Handle("POST /api/users/invite", guard.Invite(cfg), web.Permission("users.manage"))
	reg.Handle("GET /api/users", guard.AccountDirectory(cfg), web.Permission("users.manage"))
	reg.Handle("PUT /api/users/{id}", http.HandlerFunc(guard.UpdateAccount), web.Permission("users.manage"))
	reg.Handle("PUT /api/roles/{id}", guard.SaveRole(cfg), web.Permission("users.manage"))
	reg.Handle("POST /api/auth/reset-request", guard.RequestReset(cfg), web.Public())
	reg.Handle("POST /api/auth/password", guard.SetPassword(cfg), web.Public())
	registerSystemRoutes(reg, pool, cfg)
	reg.Handle("GET /api/auth/session", http.HandlerFunc(guard.Session), web.Authenticated())
	reg.Handle("POST /api/auth/logout", http.HandlerFunc(guard.Logout), web.Authenticated())
	reg.Handle("GET /api/portal", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := web.CurrentSession(r.Context())
		pages := []map[string]any{}
		for id, page := range cfg.Pages {
			if page.Policy == "admin" && !session.User.Admin {
				continue
			}
			if page.Permission != "" && !session.User.Admin && !slices.Contains(session.User.Permissions, page.Permission) {
				continue
			}
			if page.Policy != "admin" && page.Permission == "" {
				continue
			}
			item := map[string]any{"id": id, "label": page.Label, "path": page.Path, "kind": page.Kind, "order": page.Order}
			if page.Kind == "table" {
				endpoint, ok := cfg.Endpoints[strings.TrimPrefix(page.Endpoint, "endpoint/")]
				if !ok {
					continue
				}
				item["endpoint"] = endpoint.Path
				item["columns"] = endpoint.Columns
				item["filters"] = append([]string{}, endpoint.Filters...)
				item["sort"] = append([]string{}, endpoint.Sort...)
				item["search"] = append([]string{}, endpoint.Search...)
				pageSize := endpoint.PageSize
				if pageSize == 0 {
					pageSize = 50
				}
				item["page_size"] = pageSize
				item["shape"] = "list"
				if endpoint.Shape == "singleton" {
					item["shape"] = "singleton"
				}
				if endpoint.Export != nil {
					item["export"] = endpoint.Export
				}
			}
			pages = append(pages, item)
		}
		sort.Slice(pages, func(i, j int) bool {
			if pages[i]["order"].(int) != pages[j]["order"].(int) {
				return pages[i]["order"].(int) < pages[j]["order"].(int)
			}
			return pages[i]["path"].(string) < pages[j]["path"].(string)
		})
		httpx.Write(w, 200, map[string]any{"display_name": cfg.Ddp.DisplayName, "pages": pages})
	}), web.Authenticated())
	if err := endpoints.Register(reg, pool, cfg); err != nil {
		return nil, nil, err
	}
	if register != nil {
		register(reg)
	}
	reg.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpx.Fail(w, 404, "not_found", "Endpoint not found") }), web.Public())
	reg.Handle("/metrics", http.NotFoundHandler(), web.Public())
	reg.Handle("/", frontend.Handler(), web.Public())
	return reg, guard, nil
}

func Routes(cfg *config.Config, register func(*web.Registry)) ([]web.RouteInfo, error) {
	reg, guard, err := portalRegistry(nil, cfg, register)
	if err != nil {
		return nil, err
	}
	if _, err := reg.Build(guard); err != nil {
		return nil, err
	}
	return reg.Routes(), nil
}

type Portal struct {
	http.Handler
	Metrics http.Handler
}

func PortalHandler(pool *pgxpool.Pool, cfg *config.Config, logger *slog.Logger, register func(*web.Registry)) (*Portal, error) {
	reg, guard, err := portalRegistry(pool, cfg, register)
	if err != nil {
		return nil, err
	}
	handler, err := reg.Build(guard)
	if err != nil {
		return nil, err
	}
	handler = web.NewFloodGuard().Wrap(handler)
	patterns := []string{}
	for _, route := range reg.Routes() {
		patterns = append(patterns, route.Pattern)
	}
	monitor := metrics.New(pool, cfg, patterns)
	observed := web.Observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		handler.ServeHTTP(w, r)
	}), logger, monitor.ObserveHTTP)
	return &Portal{Handler: observed, Metrics: monitor.Handler()}, nil
}

func Run(ctx context.Context, cfg *config.Config, databaseURL, metricsAddr string, register func(*web.Registry)) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("invalid API database URL")
	}
	defer pool.Close()
	handler, err := PortalHandler(pool, cfg, slog.Default(), register)
	if err != nil {
		return err
	}
	return metrics.Run(ctx, metricsAddr, handler.Metrics, func(ctx context.Context) error {
		server := &http.Server{Addr: cfg.Serving.Addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute}
		done := make(chan error, 1)
		go func() { done <- server.ListenAndServe() }()
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := server.Shutdown(shutdown)
			if err != nil {
				_ = server.Close()
			}
			<-done
			return err
		}
	})
}
