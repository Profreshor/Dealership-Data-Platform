// Package endpoints serves declared reporting relations through typed policies.
package endpoints

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Register(reg *web.Registry, pool *pgxpool.Pool, cfg *config.Config) error {
	for name, endpoint := range cfg.Endpoints {
		if err := cfg.ValidateEndpoint(endpoint); err != nil {
			return fmt.Errorf("endpoint/%s: %w", name, err)
		}
		policy, err := Policy(endpoint.Policy, cfg)
		if err != nil {
			return fmt.Errorf("endpoint/%s: %w", name, err)
		}
		reg.Handle("GET "+endpoint.Path, reportingHandler(pool, name, endpoint, "list", false), policy)
		if endpoint.Shape != "singleton" {
			reg.Handle("GET "+endpoint.Path+"/row", reportingHandler(pool, name, endpoint, "get", false), policy)
			if len(endpoint.UniqueKey) == 1 {
				reg.Handle("GET "+endpoint.Path+"/rows/{id}", reportingHandler(pool, name, endpoint, "get", true), policy)
			}
		}
		if endpoint.Export != nil {
			reg.Handle("GET "+endpoint.Path+"/export.csv", reportingHandler(pool, name, endpoint, "export", false), policy)
		}
	}
	return nil
}

func reportingHandler(pool *pgxpool.Pool, name string, endpoint config.Endpoint, mode string, pathID bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			httpx.Fail(w, 400, "invalid_query", "Invalid query string")
			return
		}
		if pathID {
			if len(params) > 0 {
				httpx.Fail(w, 400, "invalid_query", "Path lookup does not accept query parameters")
				return
			}
			params.Set("key."+endpoint.UniqueKey[0], r.PathValue("id"))
		}
		query, err := Plan("endpoint/"+name, endpoint, params, mode)
		if err != nil {
			httpx.Fail(w, 400, "invalid_query", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		args := query.Args
		if mode == "export" {
			args = append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)
		}
		rows, err := pool.Query(ctx, query.SQL, args...)
		if err != nil {
			queryFailure(w, err)
			return
		}
		defer rows.Close()
		if mode == "export" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".csv"}))
			w.Header().Set("X-DDP-Export-Limit", strconv.Itoa(query.Limit))
			w.Header().Set("Trailer", "X-DDP-Export-Truncated, X-DDP-Export-Rows")
			count, truncated, err := WriteCSV(w, rows, endpoint.Columns, query.Limit)
			if err != nil {
				// An incomplete CSV is not a successful response; let net/http abort it.
				panic(http.ErrAbortHandler)
			}
			w.Header().Set("X-DDP-Export-Truncated", strconv.FormatBool(truncated))
			w.Header().Set("X-DDP-Export-Rows", strconv.Itoa(count))
			return
		}
		data := []map[string]any{}
		var cursorValues []*string
		more := false
		for rows.Next() {
			if mode == "list" && endpoint.Shape != "singleton" && len(data) == query.Limit {
				more = true
				break
			}
			values, err := rows.Values()
			if err != nil {
				queryFailure(w, err)
				return
			}
			row := make(map[string]any, len(endpoint.Columns))
			for i, column := range endpoint.Columns {
				value := values[i]
				// Decimal JSON values are text to preserve business precision.
				if decimal, ok := value.(pgtype.Numeric); ok {
					value, err = decimal.Value()
					if err != nil {
						queryFailure(w, err)
						return
					}
				}
				row[column] = value
			}
			data = append(data, row)
			if mode == "list" && endpoint.Shape != "singleton" {
				cursorValues = make([]*string, len(query.Order))
				for i := range query.Order {
					if values[len(endpoint.Columns)+i] != nil {
						value, ok := values[len(endpoint.Columns)+i].(string)
						if !ok {
							queryFailure(w, errors.New("invalid cursor value"))
							return
						}
						cursorValues[i] = &value
					}
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			queryFailure(w, err)
			return
		}
		if mode == "get" || endpoint.Shape == "singleton" {
			if len(data) > 1 {
				httpx.Fail(w, 500, "contract_failed", "Reporting identity returned more than one row")
				return
			}
			if len(data) == 0 {
				httpx.Fail(w, 404, "not_found", "Row not found")
				return
			}
			httpx.Write(w, 200, data[0])
			return
		}
		var next *string
		if more {
			token, err := EncodeCursor(query, cursorValues)
			if err != nil {
				queryFailure(w, err)
				return
			}
			next = &token
		}
		httpx.Write(w, 200, httpx.Page{Rows: data, NextCursor: next})
	})
}

func queryFailure(w http.ResponseWriter, err error) {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && strings.HasPrefix(databaseError.Code, "22") {
		httpx.Fail(w, 400, "invalid_query", "Query value has an invalid type")
	} else if errors.Is(err, context.DeadlineExceeded) {
		httpx.Fail(w, 504, "query_timeout", "Reporting query timed out")
	} else {
		httpx.Fail(w, 500, "query_failed", "Reporting query failed")
	}
}

func Policy(value string, cfg *config.Config) (web.Policy, error) {
	switch value {
	case "public":
		return web.Public(), nil
	case "authenticated":
		return web.Authenticated(), nil
	case "admin":
		return web.Admin(), nil
	default:
		name, ok := strings.CutPrefix(value, "permission:")
		if ok {
			if _, exists := cfg.Permissions[name]; exists {
				return web.Permission(name), nil
			}
		}
		return web.Policy{}, fmt.Errorf("unknown policy %q", value)
	}
}
