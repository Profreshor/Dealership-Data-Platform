package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5"
)

func TestRunAllRequiresExplicitDatabaseURLs(t *testing.T) {
	err := RunAll(context.Background(), &config.Config{}, t.TempDir(), Options{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "database URLs") {
		t.Fatalf("missing URL error: %v", err)
	}
}

func TestRunSchedulerRejectsInvalidInputBeforeOpeningPool(t *testing.T) {
	err := RunScheduler(context.Background(), nil, "postgres://private", t.TempDir(), "127.0.0.1:1", nil)
	if err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("invalid config error: %v", err)
	}
	cfg := &config.Config{Scheduler: config.Scheduler{MaxWorkers: 1}}
	if err := RunScheduler(context.Background(), cfg, "not a URL", t.TempDir(), "127.0.0.1:1", nil); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

func serviceFixture(t *testing.T) (*config.Config, string, *pgx.Conn) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_service_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	child := admin.Config().Copy()
	child.Database = name
	conn, err := pgx.ConnectConfig(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if err := migrate.Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "testdata", "base", "ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Serving.Addr = freeAddr(t)
	cfg.Serving.PublicURL = "http://127.0.0.1"
	uri, err := url.Parse(raw)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	uri.Path = "/" + name
	return cfg, uri.String(), conn
}

func freeAddr(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func TestRunAllReadinessHeartbeatAndShutdown(t *testing.T) {
	cfg, databaseURL, conn := serviceFixture(t)
	apiMetrics, schedulerMetrics := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunAll(ctx, cfg, filepath.Join("..", "..", ".."), Options{APIURL: componentURL(t, databaseURL, "ddp_api"), SchedulerURL: componentURL(t, databaseURL, "ddp_scheduler"), APIMetricsAddr: apiMetrics, SchedulerMetricsAddr: schedulerMetrics}, io.Discard, nil)
	}()
	client := &http.Client{Timeout: time.Second}
	readyURL := "http://" + cfg.Serving.Addr + "/readyz"
	var ready bool
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get(readyURL)
		if err == nil {
			var body map[string]bool
			_ = json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if body["ok"] {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatalf("API never became ready")
	}
	var heartbeat int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM ops.heartbeats WHERE service_ref='service/scheduler'").Scan(&heartbeat); err != nil {
			t.Fatal(err)
		}
		if heartbeat > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if heartbeat == 0 {
		cancel()
		t.Fatalf("scheduler heartbeat not recorded: %v", <-done)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	for _, addr := range []string{cfg.Serving.Addr, apiMetrics, schedulerMetrics} {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("listener %s remained bound: %v", addr, err)
		}
		_ = listener.Close()
	}
	var locked bool
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if err := conn.QueryRow(t.Context(), "SELECT pg_try_advisory_lock($1)", platform.SchedulerLockKey).Scan(&locked); err != nil {
			t.Fatal(err)
		}
		if locked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !locked {
		t.Fatal("scheduler lock remained held")
	}
}

func TestRunAllAPIStartupFailureStopsScheduler(t *testing.T) {
	cfg, databaseURL, _ := serviceFixture(t)
	occupied, err := net.Listen("tcp", cfg.Serving.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = RunAll(ctx, cfg, filepath.Join("..", "..", ".."), Options{APIURL: componentURL(t, databaseURL, "ddp_api"), SchedulerURL: componentURL(t, databaseURL, "ddp_scheduler"), APIMetricsAddr: freeAddr(t), SchedulerMetricsAddr: freeAddr(t)}, io.Discard, nil)
	if err == nil || strings.Contains(err.Error(), databaseURL) {
		t.Fatalf("unsafe startup error: %v", err)
	}
}

func componentURL(t *testing.T, raw, role string) string {
	t.Helper()
	uri, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := uri.Query()
	q.Set("role", role)
	uri.RawQuery = q.Encode()
	return uri.String()
}
