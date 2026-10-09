package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserveRequestIDAndAccessLog(t *testing.T) {
	var logs bytes.Buffer
	h := Observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Fatal("missing request ID")
		}
		_, _ = w.Write([]byte("hello"))
	}), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
	r := httptest.NewRequest(http.MethodGet, "/secret?token=private", nil)
	r.Pattern = "/safe"
	r.Header.Set("X-Request-ID", "attacker-id")
	r.Header.Set("Authorization", "Bearer private")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "hello" || w.Header().Get("X-Request-ID") == "attacker-id" {
		t.Fatalf("response: code=%d body=%q id=%q", w.Code, w.Body, w.Header().Get("X-Request-ID"))
	}
	line := logs.String()
	for _, secret := range []string{"/secret", "private", "Authorization", "attacker-id"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log contains redacted value %q: %s", secret, line)
		}
	}
	for _, field := range []string{`"request_id"`, `"method":"GET"`, `"pattern":"/safe"`, `"status":200`, `"bytes":5`, `"aborted":false`, `"panic":false`} {
		if !strings.Contains(line, field) {
			t.Fatalf("log missing %s: %s", field, line)
		}
	}
}

func TestObserveStreamingAndPartialBytes(t *testing.T) {
	h := Observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ab"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("c"))
	}), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || w.Body.String() != "abc" || !w.Flushed {
		t.Fatalf("stream response: code=%d body=%q flushed=%v", w.Code, w.Body, w.Flushed)
	}
}

func TestObserveRecoveryAndAbort(t *testing.T) {
	t.Run("before commit", func(t *testing.T) {
		var logs bytes.Buffer
		h := Observe(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret panic") }), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "internal") || !strings.Contains(logs.String(), `"panic":true`) || strings.Contains(logs.String(), "secret panic") {
			t.Fatalf("recovery: code=%d body=%q logs=%s", w.Code, w.Body, logs.String())
		}
	})
	t.Run("after commit", func(t *testing.T) {
		var logs bytes.Buffer
		h := Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("x"))
			panic(errors.New("secret panic"))
		}), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
		defer func() {
			recovered := recover()
			if recovered != http.ErrAbortHandler || !strings.Contains(logs.String(), `"aborted":true`) || strings.Contains(logs.String(), "secret panic") {
				t.Fatalf("abort: recovered=%v logs=%s", recovered, logs.String())
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}
