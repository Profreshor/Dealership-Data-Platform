package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type metricRecord struct {
	pattern, method string
	status          int
	elapsed         time.Duration
}

func TestObserveRecordsExactlyOnce(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.Handler
		method, url string
		pattern     string
		status      int
	}{
		{name: "wildcard route", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), method: "REPORT", url: "/users/secret?token=private", pattern: "GET /users/{id}", status: http.StatusOK},
		{name: "denied route", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "denied", http.StatusForbidden) }), method: http.MethodGet, url: "/denied", pattern: "GET /denied", status: http.StatusForbidden},
		{name: "panic before commit", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private") }), method: http.MethodPost, url: "/panic", pattern: "GET /panic", status: http.StatusInternalServerError},
		{name: "panic after commit", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")); panic("private") }), method: http.MethodGet, url: "/panic", pattern: "GET /panic", status: http.StatusOK},
		{name: "aborted without headers", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }), method: http.MethodGet, url: "/abort", pattern: "GET /abort", status: 0},
		{name: "empty", handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), method: http.MethodGet, url: "/empty", pattern: "GET /empty", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []metricRecord
			h := Observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Pattern = tt.pattern
				tt.handler.ServeHTTP(w, r)
			}), slog.New(slog.NewTextHandler(io.Discard, nil)), func(pattern, method string, status int, elapsed time.Duration) {
				got = append(got, metricRecord{pattern, method, status, elapsed})
			})
			func() {
				defer func() { _ = recover() }()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tt.method, tt.url, nil))
			}()
			if len(got) != 1 {
				t.Fatalf("records=%d, want 1", len(got))
			}
			if got[0].pattern != tt.pattern || got[0].method != "OTHER" && tt.method == "REPORT" || got[0].status != tt.status || got[0].elapsed < 0 {
				t.Fatalf("record=%+v", got[0])
			}
		})
	}
}

func TestObserveRecordsStreamingResponse(t *testing.T) {
	var got metricRecord
	h := Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
	}), slog.New(slog.NewTextHandler(io.Discard, nil)), func(pattern, method string, status int, elapsed time.Duration) {
		got = metricRecord{pattern, method, status, elapsed}
	})
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if r.Code != http.StatusOK || !r.Flushed || got.status != http.StatusOK || got.pattern != "unmatched" {
		t.Fatalf("response=%d flushed=%v record=%+v", r.Code, r.Flushed, got)
	}
}
