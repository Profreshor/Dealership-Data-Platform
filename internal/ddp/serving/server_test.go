package serving

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestReadinessAgainstPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("ddp_ready_%x", []byte(rand.Text())[:10])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	settings, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	settings.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	handler := Handler(pool)
	check := func(path string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status {
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, status, w.Body.String())
		}
	}
	check("/healthz", 200)
	check("/readyz", 503)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	check("/readyz", 200)
	if _, err := pool.Exec(ctx, `INSERT INTO ddp.platform_migrations(id,checksum) VALUES('20990101000000_forward','new-image');
INSERT INTO ddp.client_migrations(id,checksum) VALUES('20990101000000_forward','new-image')`); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 200)
	if _, err := pool.Exec(ctx, "UPDATE ddp.platform_migrations SET checksum = 'changed'"); err != nil {
		t.Fatal(err)
	}
	check("/readyz", 503)
}

func TestPortalMiddlewareRejectsFloodBeforeReadingBody(t *testing.T) {
	cfg, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler, err := PortalHandler(nil, cfg, slog.New(slog.NewJSONHandler(&logs, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for i := 0; i < 61; i++ {
		var body io.Reader = strings.NewReader(`{"password":"secret-password","unexpected":true}`)
		want := 400
		if i == 60 {
			body = unreadableAuthBody{t}
			want = 429
		}
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login?q=secret-query", body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", "ddp_session=secret-cookie")
		r.Header.Set("X-Request-ID", "secret-spoofed-id")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		id := w.Header().Get("X-Request-ID")
		if _, err := ulid.ParseStrict(id); err != nil || ids[id] {
			t.Fatalf("invalid/reused request ID: %q", id)
		}
		ids[id] = true
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("response: %d %v", w.Code, w.Header())
		}
		if want == 429 && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry delay")
		}
	}
	if strings.Contains(logs.String(), "secret-") {
		t.Fatal("request secrets leaked to access log")
	}
	decoder := json.NewDecoder(&logs)
	for range 61 {
		var event struct {
			RequestID string `json:"request_id"`
		}
		if err := decoder.Decode(&event); err != nil || !ids[event.RequestID] {
			t.Fatalf("uncorrelated access log: %#v %v", event, err)
		}
		delete(ids, event.RequestID)
	}
	if len(ids) != 0 {
		t.Fatal("missing access records")
	}
}

type unreadableAuthBody struct{ t *testing.T }

func (b unreadableAuthBody) Read([]byte) (int, error) {
	b.t.Fatal("rate-limited request body was read")
	return 0, io.EOF
}
