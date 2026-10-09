package comms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/oklog/ulid/v2"
)

const smtpLimit = 30 * time.Second
const messageLimit = 1 << 20

// Send delivers one rendered message, returning errors safe to expose to callers.
func Send(ctx context.Context, settings config.SMTP, id string, recipients []string, message Rendered) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if settings.TLS == "" {
		settings.TLS = "starttls"
	}
	ctx, cancel := context.WithTimeout(ctx, smtpLimit)
	defer cancel()
	if err := validateSMTPInput(settings, id, recipients, message); err != nil {
		return err
	}
	parsedID, _ := ulid.ParseStrict(id)
	wire, err := buildMessage(id, settings.From, recipients, message, parsedID)
	if err != nil {
		return errors.New("smtp validation failed")
	}
	from, _ := mail.ParseAddress(settings.From)
	host, _, err := net.SplitHostPort(settings.Addr)
	if err != nil || host == "" {
		return errors.New("smtp connection failed")
	}

	password := ""
	if settings.Username != "" {
		password = os.Getenv(settings.PasswordEnv)
		if password == "" {
			return errors.New("smtp authentication failed")
		}
	}

	conn, err := dialSMTP(ctx, settings, host)
	if err != nil {
		return classifyNetworkError(ctx, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()
	_ = conn.SetDeadline(deadline(ctx))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return classifyNetworkError(ctx, err)
	}
	if settings.TLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp tls negotiation failed")
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return errors.New("smtp tls negotiation failed")
		}
	}
	if settings.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", settings.Username, password, host)); err != nil {
			return errors.New("smtp authentication failed")
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return errors.New("smtp sender rejected")
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return errors.New("smtp recipient rejected")
		}
	}
	w, err := client.Data()
	if err != nil {
		return errors.New("smtp data failed")
	}
	if _, err = io.Copy(w, strings.NewReader(wire)); err != nil {
		return errors.New("smtp data failed")
	}
	if err := w.Close(); err != nil {
		return errors.New("smtp data failed")
	}
	// Once DATA is accepted, a QUIT failure must not cause a retry.
	_ = client.Quit()
	return nil
}

func validateSMTPInput(s config.SMTP, id string, recipients []string, m Rendered) error {
	if _, err := ulid.ParseStrict(id); err != nil {
		return errors.New("smtp validation failed")
	}
	if err := config.ValidateSMTP(&s); err != nil {
		return errors.New("smtp validation failed")
	}
	if len(recipients) == 0 || len(recipients) > 100 {
		return errors.New("smtp validation failed")
	}
	for _, recipient := range recipients {
		if _, err := bareAddress(recipient); err != nil {
			return errors.New("smtp validation failed")
		}
	}
	if strings.TrimSpace(m.Text) == "" || m.Subject == "" || len(m.Subject) > 200 || hasControl(m.Subject) || len(m.Text)+len(m.HTML) > messageLimit-4096 {
		return errors.New("smtp validation failed")
	}
	return nil
}

func bareAddress(value string) (string, error) {
	if !config.ValidEmail(value) {
		return "", errors.New("invalid address")
	}
	a, err := mail.ParseAddress(value)
	if err != nil || a.Address != value || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid address")
	}
	return a.Address, nil
}

func mustULID(id string) ulid.ULID { u, _ := ulid.ParseStrict(id); return u }

func buildMessage(id, from string, recipients []string, m Rendered, u ulid.ULID) (string, error) {
	domain := from[strings.LastIndex(from, "@")+1:]
	hash := sha256.Sum256([]byte(id))
	boundary := "ddp-" + hex.EncodeToString(hash[:8])
	var buffer bytes.Buffer
	b := &limitedWriter{dst: &buffer, left: messageLimit}
	if _, err := fmt.Fprintf(b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\n", from, strings.Join(recipients, ", "), mime.QEncoding.Encode("UTF-8", m.Subject), time.UnixMilli(int64(u.Time())).UTC().Format(time.RFC1123Z), id, domain); err != nil {
		return "", err
	}
	if m.HTML == "" {
		if _, err := io.WriteString(b, "Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"); err != nil {
			return "", err
		}
		if err := writeQP(b, m.Text); err != nil {
			return "", err
		}
		return buffer.String(), nil
	}
	if _, err := fmt.Fprintf(b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary); err != nil {
		return "", err
	}
	mp := multipart.NewWriter(b)
	if err := mp.SetBoundary(boundary); err != nil {
		return "", err
	}
	for _, part := range []struct{ kind, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", part.kind+"; charset=UTF-8")
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := mp.CreatePart(header)
		if err != nil {
			return "", err
		}
		if err = writeQP(writer, part.body); err != nil {
			return "", err
		}
	}
	if err := mp.Close(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func writeQP(dst io.Writer, value string) error {
	writer := quotedprintable.NewWriter(dst)
	if _, err := io.WriteString(writer, value); err != nil {
		return err
	}
	return writer.Close()
}

func dialSMTP(ctx context.Context, s config.SMTP, host string) (net.Conn, error) {
	d := net.Dialer{}
	if s.TLS == "implicit" {
		conn, err := d.DialContext(ctx, "tcp", s.Addr)
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(deadline(ctx))
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		err = tlsConn.HandshakeContext(ctx)
		stop()
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return nil, err
	}
	if s.TLS == "none" {
		if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
			_ = conn.Close()
			return nil, errors.New("non-loopback plaintext smtp")
		}
	}
	return conn, nil
}

func deadline(ctx context.Context) time.Time {
	d := time.Now().Add(smtpLimit)
	if until, ok := ctx.Deadline(); ok && until.Before(d) {
		return until
	}
	return d
}

func classifyNetworkError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New("smtp timeout")
	}
	return errors.New("smtp connection failed")
}
