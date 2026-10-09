package comms

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

type probeSMTPFixture struct {
	listener net.Listener
	mu       sync.Mutex
	commands []string
}

func newProbeSMTPFixture(t *testing.T) *probeSMTPFixture {
	t.Helper()
	f := &probeSMTPFixture{}
	var err error
	f.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.listener.Close() })
	go func() {
		for {
			conn, err := f.listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *probeSMTPFixture) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 probe\r\n")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		f.mu.Lock()
		f.commands = append(f.commands, command)
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(command, "EHLO"):
			fmt.Fprint(conn, "250-probe\r\n250 OK\r\n")
		case command == "NOOP":
			fmt.Fprint(conn, "250 OK\r\n")
		case command == "QUIT":
			fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

func probeSettings(addr, tlsMode string) config.SMTP {
	return config.SMTP{Addr: addr, From: "sender@example.com", TLS: tlsMode}
}

func TestProbePlaintext(t *testing.T) {
	f := newProbeSMTPFixture(t)
	if err := Probe(context.Background(), probeSettings(f.listener.Addr().String(), "none")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(f.commands, "|")
	if !strings.Contains(joined, "EHLO") || !strings.Contains(joined, "NOOP") || !strings.Contains(joined, "QUIT") {
		t.Fatalf("probe commands = %v", f.commands)
	}
	for _, command := range f.commands {
		if strings.HasPrefix(command, "AUTH") || strings.HasPrefix(command, "MAIL") || strings.HasPrefix(command, "RCPT") || command == "DATA" {
			t.Fatalf("probe sent forbidden command %q", command)
		}
	}
}

func TestProbeFailuresAreSafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"missing STARTTLS", func() error {
			f := newProbeSMTPFixture(t)
			return Probe(context.Background(), probeSettings(f.listener.Addr().String(), "starttls"))
		}},
		{"non SMTP", func() error {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					defer conn.Close()
					fmt.Fprint(conn, "HTTP/1.1 200 OK\r\n\r\n")
				}
			}()
			return Probe(context.Background(), probeSettings(listener.Addr().String(), "none"))
		}},
		{"refused", func() error {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			return Probe(context.Background(), probeSettings(addr, "none"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "connection refused") {
				t.Fatalf("unsafe or missing error: %v", err)
			}
		})
	}
}

func TestProbeCancellationClosesConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(accepted)
			closed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		close(accepted)
		var b [1]byte
		_, err = conn.Read(b[:])
		closed <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Probe(ctx, probeSettings(listener.Addr().String(), "none")) }()
	<-accepted
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("Probe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Probe did not stop after cancellation")
	}
	if err := <-closed; err != io.EOF {
		t.Fatalf("server did not observe the cancelled socket close: %v", err)
	}
}
