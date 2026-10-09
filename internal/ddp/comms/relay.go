package comms

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type Outcome struct {
	ID      string `json:"id,omitempty"`
	Status  string `json:"status"`
	Attempt int    `json:"attempt,omitempty"`
}

// RelayOne owns a dedicated session lock through claim, rendering and SMTP.
// ponytail: one SMTP exchange at a time; add bounded relay workers if measured delivery latency requires it.
func RelayOne(ctx context.Context, pool *pgxpool.Pool, cfg config.Comms, root, ref string) (Outcome, error) {
	target := ""
	if ref != "" {
		var err error
		target, err = messageID(ref)
		if err != nil {
			return Outcome{}, err
		}
	}
	if err := config.ValidateSMTP(cfg.SMTP); err != nil {
		return Outcome{}, err
	}
	if cfg.SMTP.PasswordEnv != "" && os.Getenv(cfg.SMTP.PasswordEnv) == "" {
		return Outcome{}, errors.New("SMTP password environment variable is missing")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return Outcome{}, dbError(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('ddp:comms_relay',0))`).Scan(&locked); err != nil {
		return Outcome{}, dbError(err)
	}
	if !locked {
		return Outcome{Status: "busy"}, nil
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Outcome{}, dbError(err)
	}
	defer tx.Rollback(ctx)
	// Owning the session lock proves there is no previous database owner left.
	// A lost SMTP acknowledgement can still cause duplicate delivery on retry.
	_, err = tx.Exec(ctx, `UPDATE ops.deliveries SET status='interrupted',finished_at=clock_timestamp(),error='relay interrupted' WHERE status='delivering'; UPDATE ops.outbox SET status=CASE WHEN attempts<max_attempts THEN 'pending' ELSE 'failed' END,available_at=clock_timestamp()+interval '30 seconds',finished_at=CASE WHEN attempts>=max_attempts THEN clock_timestamp() ELSE NULL END,last_error='relay interrupted' WHERE status='delivering'`)
	if err != nil {
		return Outcome{}, dbError(err)
	}
	var id, template, sender string
	var data []byte
	var recipients []string
	var renderedAt *time.Time
	var subject, plain, html *string
	var attempts, maxAttempts int
	err = tx.QueryRow(ctx, `SELECT id,template,context,sender,recipients,rendered_at,subject,text_body,html_body,attempts,max_attempts FROM ops.outbox WHERE status='pending' AND content_expired_at IS NULL AND available_at<=clock_timestamp() AND ($1='' OR id=$1) ORDER BY available_at,id LIMIT 1 FOR UPDATE`, target).Scan(&id, &template, &data, &sender, &recipients, &renderedAt, &subject, &plain, &html, &attempts, &maxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{Status: "idle"}, dbError(tx.Commit(ctx))
	}
	if err != nil {
		return Outcome{}, dbError(err)
	}
	attempts++
	attemptID := ulid.Make().String()
	if _, err = tx.Exec(ctx, `INSERT INTO ops.deliveries(id,message_id,number,status) VALUES($1,$2,$3,'delivering')`, attemptID, id, attempts); err != nil {
		return Outcome{}, dbError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE ops.outbox SET status='delivering',attempts=$2 WHERE id=$1`, id, attempts); err != nil {
		return Outcome{}, dbError(err)
	}
	var message Rendered
	var renderErr error
	if renderedAt == nil {
		var values map[string]any
		if err = json.Unmarshal(data, &values); err != nil {
			renderErr = errors.New("invalid saved message context")
		} else {
			message, renderErr = Render(root, template, values)
		}
		if renderErr == nil {
			_, err = tx.Exec(ctx, `UPDATE ops.outbox SET rendered_at=clock_timestamp(),subject=$2,text_body=$3,html_body=$4 WHERE id=$1`, id, message.Subject, message.Text, message.HTML)
			if err != nil {
				return Outcome{}, dbError(err)
			}
		}
	} else {
		message = Rendered{Subject: *subject, Text: *plain, HTML: *html}
	}
	if renderErr != nil {
		if _, err = tx.Exec(ctx, `UPDATE ops.outbox SET status='failed',finished_at=clock_timestamp(),last_error=$2 WHERE id=$1`, id, renderErr.Error()); err != nil {
			return Outcome{}, dbError(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE ops.deliveries SET status='failed',finished_at=clock_timestamp(),error=$2 WHERE id=$1`, attemptID, renderErr.Error()); err != nil {
			return Outcome{}, dbError(err)
		}
		return Outcome{id, "failed", attempts}, dbError(tx.Commit(ctx))
	}
	if err = tx.Commit(ctx); err != nil {
		return Outcome{}, dbError(err)
	}
	settings := *cfg.SMTP
	settings.From = sender // Sender and recipients were resolved by the producer.
	sendErr := Send(ctx, settings, id, recipients, message)
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	finish, err := conn.Begin(finishCtx)
	if err != nil {
		return Outcome{}, dbError(err)
	}
	defer finish.Rollback(finishCtx)
	status := "delivered"
	var savedError *string
	if sendErr != nil {
		detail := sendErr.Error()
		savedError = &detail
		status = "pending"
		if attempts >= maxAttempts {
			status = "failed"
		}
	}
	delay := retryDelay(attempts)
	if _, err = finish.Exec(finishCtx, `UPDATE ops.outbox SET status=$2,last_error=$3,finished_at=CASE WHEN $2='pending' THEN NULL ELSE clock_timestamp() END,available_at=clock_timestamp()+$4::interval WHERE id=$1`, id, status, savedError, delay.String()); err != nil {
		return Outcome{}, dbError(err)
	}
	if _, err = finish.Exec(finishCtx, `UPDATE ops.deliveries SET status=CASE WHEN $2::text IS NULL THEN 'delivered' ELSE 'failed' END,error=$2,finished_at=clock_timestamp() WHERE id=$1`, attemptID, savedError); err != nil {
		return Outcome{}, dbError(err)
	}
	return Outcome{id, status, attempts}, dbError(finish.Commit(finishCtx))
}
func retryDelay(attempt int) time.Duration {
	delay := 30 * time.Second
	for i := 1; i < attempt && delay < time.Hour; i++ {
		delay *= 2
	}
	return min(delay, time.Hour)
}
