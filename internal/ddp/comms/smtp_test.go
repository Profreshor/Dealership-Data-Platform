package comms

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

type smtpFixture struct {
	listener net.Listener
	data     bool
	quitFail bool
	rcptFail bool
	dataFail bool
}

func newSMTPFixture(t *testing.T) *smtpFixture {
	t.Helper()
	f := &smtpFixture{}
	var err error
	f.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.listener.Close() })
	go func() {
		for {
			c, err := f.listener.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *smtpFixture) serve(c net.Conn) {
	defer c.Close()
	b := bufio.NewReader(c)
	fmt.Fprint(c, "220 local\r\n")
	for {
		line, err := b.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			fmt.Fprint(c, "250-local\r\n250 OK\r\n")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			fmt.Fprint(c, "250 OK\r\n")
		case strings.HasPrefix(cmd, "RCPT TO"):
			if f.rcptFail {
				fmt.Fprint(c, "550 rejected\r\n")
			} else {
				fmt.Fprint(c, "250 OK\r\n")
			}
		case cmd == "DATA":
			f.data = true
			if f.dataFail {
				fmt.Fprint(c, "550 no data\r\n")
				continue
			}
			fmt.Fprint(c, "354 go\r\n")
			for {
				line, err = b.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(line) == "." {
					break
				}
			}
			fmt.Fprint(c, "250 accepted\r\n")
		case cmd == "QUIT":
			if f.quitFail {
				fmt.Fprint(c, "421 bye\r\n")
			} else {
				fmt.Fprint(c, "221 bye\r\n")
			}
			return
		default:
			fmt.Fprint(c, "250 OK\r\n")
		}
	}
}

func fixtureSettings(f *smtpFixture) config.SMTP {
	return config.SMTP{Addr: f.listener.Addr().String(), From: "sender@example.com", TLS: "none"}
}

func TestSMTPMessageAndBoundaryValidation(t *testing.T) {
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	s := config.SMTP{Addr: "127.0.0.1:2525", From: "sender@example.com", TLS: "none"}
	m := Rendered{Subject: "Résumé", Text: "plain", HTML: "<b>plain</b>"}
	if err := validateSMTPInput(s, id, []string{"to@example.com"}, m); err != nil {
		t.Fatal(err)
	}
	got, err := buildMessage(id, s.From, []string{"to@example.com"}, m, mustULID(id))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"multipart/alternative", "quoted-printable", "Message-ID: <" + id + "@example.com>", "Subject: =?UTF-8?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("message missing %q: %s", want, got)
		}
	}
	if len(got) > messageLimit {
		t.Fatal("message exceeds limit")
	}
}

func TestSMTPFixturePipeline(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, tc := range []struct {
		name      string
		configure func(*smtpFixture)
		tls       string
		wantErr   bool
	}{
		{"happy MIME envelope", func(*smtpFixture) {}, "none", false},
		{"RCPT rejection before DATA", func(f *smtpFixture) { f.rcptFail = true }, "none", true},
		{"DATA failure", func(f *smtpFixture) { f.dataFail = true }, "none", true},
		{"QUIT failure accepted", func(f *smtpFixture) { f.quitFail = true }, "none", false},
		{"STARTTLS unavailable", func(*smtpFixture) {}, "starttls", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSMTPFixture(t)
			tc.configure(f)
			s := fixtureSettings(f)
			s.TLS = tc.tls
			err := Send(context.Background(), s, id, []string{"to@example.com"}, Rendered{Subject: "hello", Text: "body", HTML: "<b>body</b>"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Send() error = %v", err)
			}
			if tc.name == "RCPT rejection before DATA" && f.data {
				t.Fatal("DATA sent after RCPT rejection")
			}
			if tc.name == "happy MIME envelope" && !f.data {
				t.Fatal("DATA not sent")
			}
		})
	}
}

func TestSMTPCancellationBlockedGreeting(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, e := l.Accept()
		if e == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	s := config.SMTP{Addr: l.Addr().String(), From: "sender@example.com", TLS: "none"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := Send(ctx, s, "01ARZ3NDEKTSV4RRFFQ69G5FAV", []string{"to@example.com"}, Rendered{Subject: "x", Text: "x"}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestSMTPPlaintextAuthRefused(t *testing.T) {
	s := config.SMTP{Addr: "127.0.0.1:2525", From: "sender@example.com", Username: "user", PasswordEnv: "SMTP_PASSWORD", TLS: "none"}
	err := Send(context.Background(), s, "01ARZ3NDEKTSV4RRFFQ69G5FAV", []string{"to@example.com"}, Rendered{Subject: "x", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "tls policy") && !strings.Contains(err.Error(), "validation") {
		t.Fatalf("expected plaintext auth refusal, got %v", err)
	}
}

func TestSMTPRejectsInvalidRenderedContent(t *testing.T) {
	settings := config.SMTP{Addr: "127.0.0.1:2525", From: "sender@example.test", TLS: "none"}
	for _, message := range []Rendered{{Subject: "subject", Text: " "}, {Subject: "invisible\u0085control", Text: "body"}, {Subject: strings.Repeat("é", 101), Text: "body"}} {
		if err := validateSMTPInput(settings, "01ARZ3NDEKTSV4RRFFQ69G5FAV", []string{"to@example.test"}, message); err == nil {
			t.Fatal("invalid rendered content accepted")
		}
	}
}
