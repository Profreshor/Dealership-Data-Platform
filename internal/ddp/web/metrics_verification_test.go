package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestObserveMetricsCallbackUsesMuxPatternAndFinishesOnce(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("wildcard route and bounded method", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("REPORT /users/{id}", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		var records []metricRecord
		h := Observe(mux, logger, func(pattern, method string, status int, elapsed time.Duration) {
			records = append(records, metricRecord{pattern, method, status, elapsed})
		})

		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("REPORT", "/users/private?token=secret", nil))
		if len(records) != 1 || records[0].pattern != "REPORT /users/{id}" || records[0].method != "OTHER" || records[0].status != http.StatusOK || records[0].elapsed < 0 {
			t.Fatalf("records=%+v", records)
		}
	})

	tests := []struct {
		name      string
		handler   http.Handler
		status    int
		recovered any
		flush     bool
	}{
		{name: "panic before headers", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret") }), status: http.StatusInternalServerError},
		{name: "panic after headers", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")); panic("secret") }), status: http.StatusOK, recovered: http.ErrAbortHandler},
		{name: "abort before headers", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }), status: 0, recovered: http.ErrAbortHandler},
		{name: "flush", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				panic(err)
			}
		}), status: http.StatusOK, flush: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var records []metricRecord
			h := Observe(tt.handler, logger, func(pattern, method string, status int, elapsed time.Duration) {
				records = append(records, metricRecord{pattern, method, status, elapsed})
			})
			w := httptest.NewRecorder()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			}()
			if len(records) != 1 || records[0].status != tt.status || records[0].elapsed < 0 || recovered != tt.recovered {
				t.Fatalf("recovered=%v response=%d records=%+v", recovered, w.Code, records)
			}
			if tt.flush && !w.Flushed {
				t.Fatal("response was not flushed")
			}
		})
	}
}
