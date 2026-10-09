package serving

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountMessagesDeliverThroughSMTPAgainstPostgres(t *testing.T) {
	env := accountTestEnv(t)
	smtp := newAccountSMTPFixture(t)
	env.cfg.Comms.SMTP = &config.SMTP{Addr: smtp.listener.Addr().String(), From: "noreply@example.test", TLS: "none"}

	manager := portalClient(t)
	managerSession := accountLogin(t, env.server, manager, "manager@example.test", "manager-password")
	if got := portalRequest(t, manager, http.MethodPost, env.server.URL+"/api/users/invite", `{"email":"new@example.test","roles":["reader"]}`, env.origin, managerSession.CSRFToken); got.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", got.status, got.body)
	}
	welcomeToken := accountOutboxToken(t, env.owner, "welcome", "new@example.test")
	if got := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/reset-request", `{"email":"user@example.test"}`, env.origin); got.status != http.StatusAccepted {
		t.Fatalf("reset request: %d %s", got.status, got.body)
	}
	resetToken := accountOutboxToken(t, env.owner, "password-reset", "user@example.test")
	if got := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/password", `{"token":"`+resetToken+`","password":"smtp-password"}`, env.origin); got.status != http.StatusOK {
		t.Fatalf("set password: %d %s", got.status, got.body)
	}

	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		repo = filepath.Dir(repo)
	}
	schedulerConfig := env.owner.Config()
	schedulerConfig.ConnConfig.RuntimeParams["role"] = "ddp_scheduler"
	scheduler, err := pgxpool.NewWithConfig(t.Context(), schedulerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scheduler.Close)
	for range 3 {
		outcome, err := comms.RelayOne(context.Background(), scheduler, env.cfg.Comms, repo, "")
		if err != nil || outcome.Status != "delivered" {
			t.Fatalf("relay: outcome=%+v err=%v", outcome, err)
		}
	}
	messages := smtp.Messages()
	if len(messages) != 3 {
		t.Fatalf("SMTP messages = %d, want 3", len(messages))
	}
	if !strings.Contains(messages[0], "/welcome") || !strings.Contains(messages[0], welcomeToken) || strings.Contains(messages[0], "smtp-password") {
		t.Fatalf("welcome message missing link or leaked password: %s", messages[0])
	}
	if !strings.Contains(messages[1], "/reset-password") || !strings.Contains(messages[1], resetToken) || strings.Contains(messages[1], "smtp-password") {
		t.Fatalf("reset message missing link or leaked password: %s", messages[1])
	}
	if !strings.Contains(messages[2], "password") || strings.Contains(messages[2], "smtp-password") {
		t.Fatalf("password changed message unexpected: %s", messages[2])
	}
	var delivered int
	if err := env.owner.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE template IN ('welcome','password-reset','password-changed') AND status='delivered'`).Scan(&delivered); err != nil || delivered != 3 {
		t.Fatalf("delivered messages=%d err=%v", delivered, err)
	}
}

type accountSMTPFixture struct {
	listener net.Listener
	mu       sync.Mutex
	messages []string
}

func newAccountSMTPFixture(t *testing.T) *accountSMTPFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &accountSMTPFixture{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *accountSMTPFixture) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 local\r\n")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			fmt.Fprint(conn, "250-local\r\n250 OK\r\n")
		case strings.HasPrefix(command, "MAIL FROM"), strings.HasPrefix(command, "RCPT TO"):
			fmt.Fprint(conn, "250 OK\r\n")
		case command == "DATA":
			fmt.Fprint(conn, "354 go\r\n")
			var data strings.Builder
			for {
				line, err = reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(line) == "." {
					break
				}
				data.WriteString(line)
			}
			f.mu.Lock()
			f.messages = append(f.messages, data.String())
			f.mu.Unlock()
			fmt.Fprint(conn, "250 accepted\r\n")
		case command == "QUIT":
			fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

func (f *accountSMTPFixture) Messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages...)
}
