package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/oklog/ulid/v2"
)

type requestIDKey struct{}

// RequestID returns the request ID assigned by Observe, or an empty string.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Observe adds request IDs, bounded access logs, and response-aware recovery.
func Observe(next http.Handler, logger *slog.Logger, record func(pattern, method string, status int, elapsed time.Duration)) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ulid.Make().String()
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		rw := &observedWriter{ResponseWriter: w}
		started := time.Now()
		finish := func(aborted, panicSeen bool) {
			logAccess(logger, r, id, rw, time.Since(started), aborted, panicSeen, record)
		}
		defer func() {
			panicSeen, aborted := false, false
			if recovered := recover(); recovered != nil {
				panicSeen = true
				if recovered == http.ErrAbortHandler {
					aborted = true
					finish(aborted, panicSeen)
					panic(http.ErrAbortHandler)
				}
				if rw.committed {
					aborted = true
					finish(aborted, panicSeen)
					panic(http.ErrAbortHandler)
				}
				rw.Header().Del("Content-Length")
				rw.Header().Del("Content-Encoding")
				httpx.Fail(rw, http.StatusInternalServerError, "internal", "Internal error")
			}
			finish(aborted, panicSeen)
		}()
		next.ServeHTTP(rw, r)
	})
}

func logAccess(logger *slog.Logger, r *http.Request, id string, rw *observedWriter, elapsed time.Duration, aborted, panicSeen bool, record func(string, string, int, time.Duration)) {
	method := r.Method
	if !standardMethod(method) {
		method = "OTHER"
	}
	pattern := r.Pattern
	if pattern == "" {
		pattern = "unmatched"
	}
	status := rw.status
	if status == 0 && !aborted {
		status = http.StatusOK
	}
	logger.Info("http request", "request_id", id, "method", method, "pattern", pattern, "status", status, "bytes", rw.bytes, "duration_ms", float64(elapsed.Microseconds())/1000, "aborted", aborted, "panic", panicSeen)
	if record != nil {
		record(pattern, method, status, elapsed)
	}
}

func standardMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

type observedWriter struct {
	http.ResponseWriter
	status    int
	bytes     int64
	committed bool
}

func (w *observedWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.ResponseWriter.WriteHeader(status)
	w.committed = true
	w.status = status
}

func (w *observedWriter) Write(p []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.NewResponseController reach the underlying writer.
func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// FlushError commits a streaming response before flushing it.
func (w *observedWriter) FlushError() error {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
