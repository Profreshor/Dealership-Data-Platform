package serving

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/inspect"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func registerSystemRoutes(reg *web.Registry, pool *pgxpool.Pool, cfg *config.Config) {
	reg.Handle("GET /api/system/status", systemStatusHandler(pool, cfg), web.Admin())
	reg.Handle("GET /api/system/runs", systemRunsHandler(pool), web.Admin())
	reg.Handle("GET /api/system/runs/{id}/logs", systemLogsHandler(pool), web.Admin())
}

func systemStatusHandler(pool *pgxpool.Pool, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		data, err := inspect.Overview(ctx, pool, cfg)
		if err != nil {
			systemFailure(w, err)
			return
		}
		httpx.Write(w, http.StatusOK, data)
	})
}

func systemRunsHandler(pool *pgxpool.Pool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		data, err := inspect.ListRuns(ctx, pool, "", 50)
		if err != nil {
			systemFailure(w, err)
			return
		}
		httpx.Write(w, http.StatusOK, data)
	})
}

func systemLogsHandler(pool *pgxpool.Pool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := ulid.ParseStrict(id); err != nil {
			httpx.Fail(w, http.StatusBadRequest, "invalid_id", "Invalid run ID")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		data, err := inspect.Logs(ctx, pool, "execution/"+id, 100)
		if err != nil {
			systemFailure(w, err)
			return
		}
		httpx.Write(w, http.StatusOK, data)
	})
}

func systemFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpx.Fail(w, http.StatusGatewayTimeout, "query_timeout", "System query timed out")
		return
	}
	if strings.Contains(err.Error(), "not found") {
		httpx.Fail(w, http.StatusNotFound, "not_found", "Run not found")
		return
	}
	httpx.Fail(w, http.StatusInternalServerError, "query_failed", "System query failed")
}
