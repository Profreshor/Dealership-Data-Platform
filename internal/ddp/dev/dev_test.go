package dev

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

func TestRunRejectsInvalidOptionsBeforeSideEffects(t *testing.T) {
	t.Setenv("DDP_DEV_DATABASE_URL", "not-a-url")
	if err := Run(context.Background(), &config.Config{}, t.TempDir(), nil, nil, Options{Comms: true}); err == nil {
		t.Fatal("comms without scheduler was accepted")
	}
}

func TestRunUsesDisposableRoleURLsAndStopsServices(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  Options
		failVite bool
	}{
		{"defaults off and stubborn descendant", Options{}, false},
		{"scheduler without SMTP", Options{Scheduler: true}, false},
		{"scheduler and SMTP", Options{Scheduler: true, Comms: true}, false},
		{"Vite failure stops both services", Options{Scheduler: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) { runDevelopment(t, tc.options, tc.failVite) })
	}
}

func runDevelopment(t *testing.T, options Options, failVite bool) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_dev_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	t.Setenv("DDP_DEV_DATABASE_URL", u.String())
	t.Setenv("DATABASE_URL", "postgres://poisoned.invalid/db")
	t.Setenv("SCHEDULER_DATABASE_URL", "postgres://poisoned.invalid/db")
	t.Setenv("JOB_DATABASE_URL", "sentinel-job-url")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "frontend"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "vite.pid")
	childMarker := filepath.Join(root, "child.pid")
	jobMarker := filepath.Join(root, "job.url")
	exitMarker := filepath.Join(root, "exit")
	npm := filepath.Join(root, "npm")
	script := "#!/bin/sh\necho $$ > '" + marker + "'\nprintf '%s' \"$JOB_DATABASE_URL\" > '" + jobMarker + "'\ntrap 'exit 0' TERM INT\n"
	if !options.Scheduler {
		script += "/bin/sh -c 'trap \"\" TERM; echo $$ > \"$1\"; while :; do sleep 1; done' child '" + childMarker + "' &\n"
	}
	script += "while :; do if [ -e '" + exitMarker + "' ]; then exit 7; fi; sleep 0.1; done\n"
	if err := os.WriteFile(npm, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	original, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	original.Serving.Addr, original.Serving.PublicURL, original.Serving.SessionTTL = developmentAddr(t), "http://127.0.0.1", "1h"
	smtp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer smtp.Close()
	contacted := make(chan struct{}, 1)
	go func() {
		conn, err := smtp.Accept()
		if err == nil {
			_ = conn.Close()
			contacted <- struct{}{}
		}
	}()
	original.Comms.SMTP = &config.SMTP{Addr: smtp.Addr().String(), From: "dev@example.test", TLS: "none"}
	before := original.Comms.SMTP
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("runtime cleanup timed out")
			}
		}
	}()
	options.APIMetricsAddr, options.SchedulerMetricsAddr = developmentAddr(t), developmentAddr(t)
	go func() {
		done <- Run(ctx, original, root, io.Discard, nil, options)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("development stopped before Vite marker: %v", err)
		default:
		}
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Vite marker not written")
		}
		time.Sleep(20 * time.Millisecond)
	}
	child, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close(context.Background())
	client := &http.Client{Timeout: time.Second}
	waitDevelopment(t, func() bool {
		response, err := client.Get("http://" + original.Serving.Addr + "/readyz")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == 200
	})
	if options.Scheduler {
		waitDevelopment(t, func() bool {
			var state string
			err := child.QueryRow(t.Context(), "SELECT state FROM ops.heartbeats WHERE service_ref='service/scheduler'").Scan(&state)
			return err == nil && state == "running"
		})
	} else {
		var count int
		if err := child.QueryRow(t.Context(), "SELECT count(*) FROM ops.heartbeats").Scan(&count); err != nil || count != 0 {
			t.Fatalf("default scheduler started: %d %v", count, err)
		}
	}
	jobData, err := os.ReadFile(jobMarker)
	jobURL, roleErr := roleURL(u.String(), "ddp_job")
	if err != nil || roleErr != nil || string(jobData) != jobURL {
		t.Fatal("Vite inherited the wrong job database")
	}
	// Save rendered synthetic mail before triggering a scheduler relay tick.
	messageID := ulid.Make().String()
	if _, err := child.Exec(t.Context(), `INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients,rendered_at,subject,text_body,html_body)
VALUES($1,'dev-probe',decode(repeat('00',32),'hex'),'probe','{}','dev@example.test',ARRAY['recipient@example.test'],clock_timestamp(),'probe','probe','<p>probe</p>')`, messageID); err != nil {
		t.Fatal(err)
	}
	if options.Scheduler {
		// A queued relay from an earlier opt-in must also remain unable to send when disabled.
		if _, err := child.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,scheduled_at,status,dispatch) VALUES($1,'ddp:comms_relay',clock_timestamp(),'queued','scheduler')`, ulid.Make().String()); err != nil {
			t.Fatal(err)
		}
	}
	if options.Comms {
		select {
		case <-contacted:
		case <-time.After(5 * time.Second):
			var detail string
			_ = child.QueryRow(t.Context(), "SELECT coalesce(last_error,'no error') FROM ops.outbox WHERE id=$1", messageID).Scan(&detail)
			t.Fatalf("opted-in relay never reached SMTP: %s", detail)
		}
	} else {
		if options.Scheduler {
			waitDevelopment(t, func() bool {
				var failed int
				err := child.QueryRow(t.Context(), "SELECT count(*) FROM ops.executions WHERE job_ref='ddp:comms_relay' AND status='failed'").Scan(&failed)
				return err == nil && failed > 0
			})
		}
		select {
		case <-contacted:
			t.Fatal("disabled communications contacted SMTP")
		default:
		}
	}
	if failVite {
		if err := os.WriteFile(exitMarker, nil, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		cancel()
	}
	select {
	case err := <-done:
		stopped = true
		if failVite {
			if err == nil || !strings.Contains(err.Error(), "exit status 7") {
				t.Fatalf("Vite failure was lost: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("development services did not stop")
	}
	for _, addr := range []string{original.Serving.Addr, options.APIMetricsAddr, options.SchedulerMetricsAddr} {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("listener remained open: %s %v", addr, err)
		}
		_ = listener.Close()
	}
	for _, file := range []string{marker, childMarker} {
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) && file == childMarker && options.Scheduler {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid < 1 {
			t.Fatalf("invalid PID marker: %q", data)
		}
		waitDevelopment(t, func() bool {
			state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
			return err != nil || strings.TrimSpace(string(state)) == "" || strings.HasPrefix(strings.TrimSpace(string(state)), "Z")
		})
	}
	if got := os.Getenv("JOB_DATABASE_URL"); got != "sentinel-job-url" {
		t.Fatalf("JOB_DATABASE_URL=%q", got)
	}
	if original.Comms.SMTP != before {
		t.Fatal("development run mutated original config")
	}
}

func TestDevelopmentDatabaseURLIsPostgresAndRolesAreDerived(t *testing.T) {
	for _, raw := range []string{"", "host=localhost dbname=ddp", "https://example.test/db"} {
		if raw == "" {
			continue
		}
		if err := validateURL(raw); err == nil {
			t.Fatalf("accepted non-PostgreSQL URL %q", raw)
		}
	}
	url, err := roleURL(DatabaseURL, "ddp_job")
	if err != nil || url == DatabaseURL || !strings.Contains(url, "role=ddp_job") {
		t.Fatalf("derived role URL=%q err=%v", url, err)
	}
}

func developmentAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func waitDevelopment(t *testing.T, ready func() bool) {
	t.Helper()
	for until := time.Now().Add(6 * time.Second); time.Now().Before(until); {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("development state did not converge")
}
