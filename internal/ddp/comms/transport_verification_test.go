package comms

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

type capturedSMTP struct {
	commands []string
	data     string
}

func captureSMTP(t *testing.T, dataReply string) (config.SMTP, <-chan capturedSMTP) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	done := make(chan capturedSMTP, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		_, _ = io.WriteString(c, "220 local ESMTP\r\n")
		var got capturedSMTP
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				done <- got
				return
			}
			command := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			got.commands = append(got.commands, command)
			switch {
			case strings.HasPrefix(command, "EHLO "):
				_, _ = io.WriteString(c, "250-local\r\n250 OK\r\n")
			case strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
				_, _ = io.WriteString(c, "250 OK\r\n")
			case command == "DATA":
				_, _ = io.WriteString(c, "354 continue\r\n")
				var body strings.Builder
				for {
					line, err = r.ReadString('\n')
					if err != nil {
						done <- got
						return
					}
					if line == ".\r\n" {
						break
					}
					body.WriteString(strings.TrimPrefix(line, "."))
				}
				got.data = body.String()
				_, _ = io.WriteString(c, dataReply+"\r\n")
				if !strings.HasPrefix(dataReply, "2") {
					done <- got
					return
				}
			case command == "QUIT":
				// A failed QUIT must not turn an acknowledged message into a retry.
				_, _ = io.WriteString(c, "421 closing\r\n")
				done <- got
				return
			default:
				_, _ = io.WriteString(c, "500 unexpected\r\n")
			}
		}
	}()
	return config.SMTP{Addr: l.Addr().String(), From: "sender@example.com", TLS: "none"}, done
}

func TestTransportVerificationCapturesEnvelopeAndMIME(t *testing.T) {
	settings, done := captureSMTP(t, "250 queued")
	err := Send(context.Background(), settings, "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		[]string{"first@example.com", "second@example.com"},
		Rendered{Subject: "Résumé", Text: "plain = body", HTML: `<b>safe &amp; sound</b>`})
	if err != nil {
		t.Fatal(err)
	}
	got := <-done
	wantCommands := []string{
		"MAIL FROM:<sender@example.com>",
		"RCPT TO:<first@example.com>",
		"RCPT TO:<second@example.com>",
		"DATA",
	}
	positions := make([]int, len(wantCommands))
	for i, want := range wantCommands {
		positions[i] = -1
		for j, command := range got.commands {
			if command == want {
				positions[i] = j
				break
			}
		}
		if positions[i] < 0 || i > 0 && positions[i] <= positions[i-1] {
			t.Fatalf("SMTP command order %v, want ordered %v", got.commands, wantCommands)
		}
	}

	msg, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatalf("parse captured message: %v\n%s", err, got.data)
	}
	if msg.Header.Get("From") != "sender@example.com" || msg.Header.Get("To") != "first@example.com, second@example.com" {
		t.Fatalf("captured envelope headers: From=%q To=%q", msg.Header.Get("From"), msg.Header.Get("To"))
	}
	if subject, err := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject")); err != nil || subject != "Résumé" {
		t.Fatalf("decoded subject = %q, %v", subject, err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, %v", msg.Header.Get("Content-Type"), err)
	}
	mp := multipart.NewReader(msg.Body, params["boundary"])
	var bodies []string
	for {
		part, err := mp.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
	}
	if len(bodies) != 2 || bodies[0] != "plain = body" || bodies[1] != `<b>safe &amp; sound</b>` {
		t.Fatalf("decoded MIME bodies = %#v", bodies)
	}
}

func TestTransportVerificationRejectsDataNAKAndSanitizesError(t *testing.T) {
	settings, done := captureSMTP(t, "550 provider-secret-detail")
	err := Send(context.Background(), settings, "01ARZ3NDEKTSV4RRFFQ69G5FAV", []string{"to@example.com"}, Rendered{Subject: "x", Text: "x"})
	if err == nil || strings.Contains(err.Error(), "provider-secret-detail") {
		t.Fatalf("Send() error = %q", err)
	}
	<-done
}

func TestTransportVerificationRejectsHeadersAndOversizeBeforeDial(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			_ = c.Close()
			accepted <- struct{}{}
		}
	}()
	settings := config.SMTP{Addr: l.Addr().String(), From: "sender@example.com", TLS: "none"}
	cases := []struct {
		recipients []string
		message    Rendered
	}{
		{[]string{"\"safe@example.com\r\nBcc: victim@example.com\""}, Rendered{Subject: "x", Text: "x"}},
		{[]string{"to@example.com"}, Rendered{Subject: "ok\r\nBcc: victim@example.com", Text: "x"}},
		{[]string{"to@example.com"}, Rendered{Subject: "x", Text: strings.Repeat("x", messageLimit)}},
		// Raw input passes the cheap limit, but quoted-printable output exceeds 1 MiB.
		{[]string{"to@example.com"}, Rendered{Subject: "x", Text: strings.Repeat("=", 400_000)}},
	}
	for _, tc := range cases {
		if err := Send(context.Background(), settings, "01ARZ3NDEKTSV4RRFFQ69G5FAV", tc.recipients, tc.message); err == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	select {
	case <-accepted:
		t.Fatal("validation failure reached SMTP peer")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestTransportVerificationAlertDetailsAreOptional(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Render(root, "alert", map[string]any{
		"Severity": "warning", "Title": "Freshness", "Message": "Late", "OccurredAt": "today",
	})
	if err != nil {
		t.Fatalf("alert without optional Details: %v", err)
	}
}

func TestTransportVerificationNoPasswordInErrors(t *testing.T) {
	const env = "DDP_TRANSPORT_VERIFICATION_PASSWORD"
	t.Setenv(env, "never expose this password")
	settings := config.SMTP{Addr: "127.0.0.1:1", From: "sender@example.com", TLS: "starttls", Username: "user", PasswordEnv: env}
	err := Send(context.Background(), settings, "01ARZ3NDEKTSV4RRFFQ69G5FAV", []string{"to@example.com"}, Rendered{Subject: "x", Text: "x"})
	if err == nil || strings.Contains(err.Error(), os.Getenv(env)) {
		t.Fatalf("Send() error = %q", err)
	}
}

func TestTransportVerificationTLS(t *testing.T) {
	cert, caFile := localCertificate(t)
	for _, mode := range []string{"starttls", "implicit"} {
		t.Run(mode, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			serverErr := make(chan error, 1)
			go func() { serverErr <- serveTLSConversation(l, mode, cert) }()

			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestTransportVerificationTLSHelper$")
			cmd.Env = append(os.Environ(),
				"SSL_CERT_FILE="+caFile,
				"DDP_TLS_HELPER_MODE="+mode,
				"DDP_TLS_HELPER_ADDR="+l.Addr().String(),
				"DDP_TLS_HELPER_PASSWORD=password",
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("TLS client: %v\n%s", err, output)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("untrusted certificate rejected", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() { _ = serveTLSConversation(l, "implicit", cert) }()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestTransportVerificationTLSHelper$")
		cmd.Env = append(os.Environ(),
			"DDP_TLS_HELPER_MODE=implicit",
			"DDP_TLS_HELPER_ADDR="+l.Addr().String(),
			"DDP_TLS_HELPER_PASSWORD=password",
			"DDP_TLS_HELPER_WANT_FAIL=1",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("untrusted TLS client: %v\n%s", err, output)
		}
	})
}

func TestTransportVerificationTLSHelper(t *testing.T) {
	mode := os.Getenv("DDP_TLS_HELPER_MODE")
	if mode == "" {
		t.Skip("helper subprocess only")
	}
	settings := config.SMTP{
		Addr: os.Getenv("DDP_TLS_HELPER_ADDR"), From: "sender@example.com", TLS: mode,
		Username: "user", PasswordEnv: "DDP_TLS_HELPER_PASSWORD",
	}
	err := Send(context.Background(), settings, "01ARZ3NDEKTSV4RRFFQ69G5FAV", []string{"to@example.com"}, Rendered{Subject: "x", Text: "x"})
	if os.Getenv("DDP_TLS_HELPER_WANT_FAIL") != "" {
		if err == nil {
			t.Fatal("untrusted certificate accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

func localCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "DDP test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, caFile
}

func serveTLSConversation(l net.Listener, mode string, cert tls.Certificate) error {
	c, err := l.Accept()
	if err != nil {
		return err
	}
	defer c.Close()
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if mode == "implicit" {
		c = tls.Server(c, tlsConfig)
		if err := c.(*tls.Conn).Handshake(); err != nil {
			return err
		}
	}
	r := bufio.NewReader(c)
	if _, err := io.WriteString(c, "220 local ESMTP\r\n"); err != nil {
		return err
	}
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "EHLO ") {
		return fmt.Errorf("initial SMTP command %q: %v", line, err)
	}
	if mode == "starttls" {
		if _, err := io.WriteString(c, "250-local\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n"); err != nil {
			return err
		}
		line, err = r.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "STARTTLS" {
			return fmt.Errorf("pre-TLS command %q: %v", line, err)
		}
		if _, err := io.WriteString(c, "220 begin TLS\r\n"); err != nil {
			return err
		}
		c = tls.Server(c, tlsConfig)
		if err := c.(*tls.Conn).Handshake(); err != nil {
			return err
		}
		r = bufio.NewReader(c)
		line, err = r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "EHLO ") {
			return fmt.Errorf("post-TLS SMTP command %q: %v", line, err)
		}
	}
	if _, err := io.WriteString(c, "250-local\r\n250 AUTH PLAIN\r\n"); err != nil {
		return err
	}
	for _, prefix := range []string{"AUTH PLAIN ", "MAIL FROM:", "RCPT TO:", "DATA"} {
		line, err = r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, prefix) {
			return fmt.Errorf("SMTP command %q, want %q: %v", line, prefix, err)
		}
		if prefix == "DATA" {
			_, err = io.WriteString(c, "354 continue\r\n")
		} else if prefix == "AUTH PLAIN " {
			_, err = io.WriteString(c, "235 authenticated\r\n")
		} else {
			_, err = io.WriteString(c, "250 OK\r\n")
		}
		if err != nil {
			return err
		}
	}
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			return err
		}
		if line == ".\r\n" {
			break
		}
	}
	if _, err := io.WriteString(c, "250 queued\r\n"); err != nil {
		return err
	}
	line, err = r.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "QUIT" {
		return fmt.Errorf("final SMTP command %q: %v", line, err)
	}
	_, err = io.WriteString(c, "221 bye\r\n")
	return err
}
