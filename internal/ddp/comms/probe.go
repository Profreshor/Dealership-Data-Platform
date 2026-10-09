package comms

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

const probeLimit = 3 * time.Second

// Probe verifies that an SMTP server can be reached and accepts a basic session.
// It never authenticates or sends a message.
func Probe(ctx context.Context, settings config.SMTP) error {
	if err := config.ValidateSMTP(&settings); err != nil {
		return errors.New("smtp validation failed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, probeLimit)
	defer cancel()
	host, _, err := net.SplitHostPort(settings.Addr)
	if err != nil || host == "" {
		return errors.New("smtp connection failed")
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
	if err := client.Hello("localhost"); err != nil {
		return classifySMTPProbeError(ctx, err)
	}
	if settings.TLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp tls negotiation failed")
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return classifySMTPProbeError(ctx, err)
		}
	}
	if err := client.Noop(); err != nil {
		return classifySMTPProbeError(ctx, err)
	}
	if err := client.Quit(); err != nil {
		return classifySMTPProbeError(ctx, err)
	}
	return nil
}

func classifySMTPProbeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New("smtp timeout")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("smtp timeout")
	}
	return errors.New("smtp protocol failed")
}
