package web

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserveVerificationSemantics(t *testing.T) {
	t.Run("implicit status and mux pattern", func(t *testing.T) {
		var logs bytes.Buffer
		Observe(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), slog.New(slog.NewJSONHandler(&logs, nil)), nil).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/empty", nil))
		if !strings.Contains(logs.String(), `"status":200`) || !strings.Contains(logs.String(), `"bytes":0`) {
			t.Fatalf("no-write access record: %s", logs.String())
		}
		logs.Reset()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		Observe(mux, slog.New(slog.NewJSONHandler(&logs, nil)), nil).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/secret", nil))
		line := logs.String()
		if !strings.Contains(line, `"status":200`) || !strings.Contains(line, `"bytes":2`) || !strings.Contains(line, `"pattern":"GET /users/{id}"`) || strings.Contains(line, "secret") {
			t.Fatalf("unsafe or incorrect access record: %s", line)
		}
	})

	t.Run("interim status and partial write", func(t *testing.T) {
		var logs bytes.Buffer
		w := &partialWriter{header: make(http.Header)}
		Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusEarlyHints)
			_, _ = w.Write([]byte("abc"))
		}), slog.New(slog.NewJSONHandler(&logs, nil)), nil).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.status != http.StatusOK || w.interim != http.StatusEarlyHints || !strings.Contains(logs.String(), `"status":200`) || !strings.Contains(logs.String(), `"bytes":2`) {
			t.Fatalf("status=%d interim=%d logs=%s", w.status, w.interim, logs.String())
		}
	})

	t.Run("abort before and after headers", func(t *testing.T) {
		for _, after := range []bool{false, true} {
			var logs bytes.Buffer
			rr := httptest.NewRecorder()
			h := Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if after {
					_, _ = w.Write([]byte("x"))
				}
				panic(http.ErrAbortHandler)
			}), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
			}()
			if recovered != http.ErrAbortHandler {
				t.Fatalf("after=%v recovered=%v", after, recovered)
			}
			wantBody := ""
			if after {
				wantBody = "x"
			}
			if rr.Body.String() != wantBody {
				t.Fatalf("after=%v body=%q", after, rr.Body.String())
			}
			if !strings.Contains(logs.String(), `"aborted":true`) || !strings.Contains(logs.String(), `"panic":true`) || strings.Contains(logs.String(), "ErrAbortHandler") {
				t.Fatalf("after=%v logs=%s", after, logs.String())
			}
		}
	})

	t.Run("flush then panic aborts committed response", func(t *testing.T) {
		var logs bytes.Buffer
		h := Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				panic(err)
			}
			panic("private panic")
		}), slog.New(slog.NewJSONHandler(&logs, nil)), nil)
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
		if recovered != http.ErrAbortHandler {
			t.Fatalf("recovered=%v", recovered)
		}
		if !strings.Contains(logs.String(), `"aborted":true`) || strings.Contains(logs.String(), "private panic") {
			t.Fatal(logs.String())
		}
	})

	t.Run("invalid status is recovered and headers are scrubbed", func(t *testing.T) {
		w := &rejectingWriter{header: make(http.Header)}
		Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "99")
			w.Header().Set("Content-Encoding", "secret")
			w.WriteHeader(99)
		}), slog.New(slog.NewTextHandler(io.Discard, nil)), nil).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.status != http.StatusInternalServerError || w.Header().Get("Content-Length") != "" || w.Header().Get("Content-Encoding") != "" {
			t.Fatalf("status=%d headers=%v", w.status, w.Header())
		}
	})
}

func TestObserveAbortClosesRealTransport(t *testing.T) {
	s := httptest.NewServer(Observe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	}), slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	defer s.Close()
	r, err := http.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, readErr := io.ReadAll(r.Body)
	if string(body) != "partial" || readErr == nil {
		t.Fatalf("body=%q readErr=%v", body, readErr)
	}
}

type partialWriter struct {
	header          http.Header
	status, interim int
}

func (w *partialWriter) Header() http.Header { return w.header }
func (w *partialWriter) WriteHeader(status int) {
	if status < 200 {
		w.interim = status
	} else if w.status == 0 {
		w.status = status
	}
}
func (w *partialWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(p) - 1, nil
}

type rejectingWriter struct {
	header http.Header
	status int
}

func (w *rejectingWriter) Header() http.Header { return w.header }
func (w *rejectingWriter) WriteHeader(status int) {
	if status < 100 || status > 999 {
		panic(errors.New("invalid status"))
	}
	if w.status == 0 {
		w.status = status
	}
}
func (w *rejectingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(p), nil
}
