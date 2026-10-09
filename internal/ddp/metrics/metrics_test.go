package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPLabelsAreBoundedAndRuntimeMetricsSurviveDatabaseFailure(t *testing.T) {
	m := New(nil, nil, []string{"GET /api/customers/{id}"})
	for i := range 1000 {
		m.ObserveHTTP(fmt.Sprintf("/customer/secret-%d", i), fmt.Sprintf("METHOD-%d", i), 700+i, time.Millisecond)
	}
	m.ObserveHTTP("GET /api/customers/{id}", "GET", 200, time.Second)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{"go_goroutines", "ddp_metrics_database_up 0", `ddp_http_request_duration_seconds_count{method="OTHER",route="unmatched",status="other"} 1000`, `ddp_http_request_duration_seconds_sum{method="GET",route="GET /api/customers/{id}",status="200"} 1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if w.Code != 200 || strings.Contains(body, "secret-") || strings.Contains(body, "METHOD-") || strings.Contains(body, "ddp_job_executions_total{") {
		t.Fatalf("unsafe scrape: %d %s", w.Code, body)
	}
	w = httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/private", nil))
	if w.Code != 404 {
		t.Fatalf("unexpected private route: %d", w.Code)
	}
}

func TestPrivateListenerLifecycle(t *testing.T) {
	t.Run("cancel before listener starts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		started := false
		err := Run(ctx, "127.0.0.1:0", http.NotFoundHandler(), func(context.Context) error { started = true; return nil })
		if err != nil || started {
			t.Fatalf("cancelled startup: started=%t err=%v", started, err)
		}
	})
	t.Run("occupied address prevents workload", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		started := false
		err = Run(t.Context(), ln.Addr().String(), http.NotFoundHandler(), func(context.Context) error { started = true; return nil })
		if err == nil || started {
			t.Fatalf("bind failure err=%v started=%t", err, started)
		}
	})
	t.Run("workload error propagates", func(t *testing.T) {
		want := errors.New("workload failed")
		if err := Run(t.Context(), "127.0.0.1:0", http.NotFoundHandler(), func(context.Context) error { return want }); !errors.Is(err, want) {
			t.Fatalf("workload error: %v", err)
		}
	})
	t.Run("cancel stops workload and listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, addr, New(nil, nil, nil).Handler(), func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("workload did not start")
		}
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://" + addr + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("scrape: %d", response.StatusCode)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("lifecycle did not stop")
		}
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("metrics socket leaked: %v", err)
		}
		ln.Close()
	})
}
