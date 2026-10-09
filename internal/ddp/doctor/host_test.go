package doctor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPortFreeAndReady(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	if got := Port(context.Background(), addr); got.State != "ok" {
		t.Fatalf("free port state = %q, want ok", got.State)
	}

	listener, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	if got := Port(context.Background(), addr); got.State != "ok" {
		t.Fatalf("ready port state = %q: %s", got.State, got.Message)
	}
}

func TestPortBusyWrongResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false}`))
	})}
	go server.Serve(listener)
	defer server.Close()
	if got := Port(context.Background(), listener.Addr().String()); got.State != "failing" {
		t.Fatalf("wrong response state = %q: %s", got.State, got.Message)
	}
}

func TestPortCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Port(ctx, listener.Addr().String()); got.State != "unknown" {
		t.Fatalf("cancelled state = %q, want unknown", got.State)
	}
}

func TestDockerFixtures(t *testing.T) {
	dir := t.TempDir()
	writeDocker := func(name, script string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeDocker("docker", "exit 0")
	t.Setenv("PATH", dir)
	if got := Docker(context.Background()); got.State != "ok" {
		t.Fatalf("successful docker state = %q", got.State)
	}
	writeDocker("docker", "exit 1")
	if got := Docker(context.Background()); got.State != "failing" {
		t.Fatalf("failed docker state = %q", got.State)
	}
	if err := os.Remove(filepath.Join(dir, "docker")); err != nil {
		t.Fatal(err)
	}
	if got := Docker(context.Background()); got.State != "unknown" {
		t.Fatalf("missing docker state = %q", got.State)
	}
}

func TestDockerCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	got := Docker(ctx)
	if got.State != "unknown" || time.Since(started) > time.Second {
		t.Fatalf("cancelled docker = %#v, elapsed %s", got, time.Since(started))
	}
}

func TestPortRejectsRemoteAddress(t *testing.T) {
	if got := Port(context.Background(), fmt.Sprintf("example.com:%d", 80)); got.State != "unknown" {
		t.Fatalf("remote address state = %q", got.State)
	}
}
