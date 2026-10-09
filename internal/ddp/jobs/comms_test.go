package jobs

import (
	"context"
	"io"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestCommsRelayExecution(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var delivered atomic.Int32
	var stall atomic.Bool
	entered := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				wire := textproto.NewConn(conn)
				_ = wire.PrintfLine("220 synthetic SMTP")
				for {
					line, err := wire.ReadLine()
					if err != nil {
						return
					}
					switch line {
					case "DATA":
						_ = wire.PrintfLine("354 send")
						if _, err := io.Copy(io.Discard, wire.DotReader()); err != nil {
							return
						}
						if stall.Load() {
							entered <- struct{}{}
							_, _ = wire.ReadLine() // Cancellation must close the actual SMTP connection.
							return
						}
						delivered.Add(1)
						_ = wire.PrintfLine("250 accepted")
					case "QUIT":
						_ = wire.PrintfLine("221 bye")
						return
					default:
						_ = wire.PrintfLine("250 OK")
					}
				}
			}()
		}
	}()
	cfg := modelJobConfig("")
	cfg.Models = nil
	cfg.Comms.SMTP = &config.SMTP{Addr: listener.Addr().String(), From: "sender@example.test", TLS: "none"}
	if err := os.MkdirAll(filepath.Join(root, "templates/comms"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "templates/comms/probe.txt"), []byte(`{{define "subject"}}Synthetic{{end}}{{define "body"}}Hello{{end}}`), 0600); err != nil {
		t.Fatal(err)
	}
	enqueue := func(key string) string {
		t.Helper()
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		id, err := comms.Enqueue(t.Context(), tx, cfg.Comms, comms.Input{EffectKey: key, Template: "probe", Recipients: []string{"recipient@example.test"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	enqueue("one")
	enqueue("two")
	run, err := Run(t.Context(), pool, cfg, root, CommsRelayRef)
	if err != nil || run.Status != "succeeded" || delivered.Load() != 2 {
		t.Fatalf("batch: %+v %d %v", run, delivered.Load(), err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT (result->'details'->'messages'->>'delivered')::int FROM ops.attempts WHERE id=$1`, run.AttemptID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("result %d %v", count, err)
	}
	// Cancellation during DATA retains the message for retry and interrupts the durable run.
	stall.Store(true)
	id := enqueue("cancel")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { run, err = Run(ctx, pool, cfg, root, CommsRelayRef); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP not reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("relay did not cancel")
	}
	if err == nil || run.Status != "interrupted" {
		t.Fatalf("cancel: %+v %v", run, err)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM ops.outbox WHERE id=$1`, id).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("cancelled message %s %v", status, err)
	}
	cfg.Comms.SMTP = nil
	run, err = Run(t.Context(), pool, cfg, root, CommsRelayRef)
	if err == nil || run.Status != "failed" {
		t.Fatalf("missing SMTP: %+v %v", run, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT max_attempts FROM ops.executions WHERE id=$1`, run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("poll retries %d %v", count, err)
	}
}
