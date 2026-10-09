package comms

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

var ErrEffectConflict = errors.New("message effect key already names different content")

type Input struct {
	EffectKey  string         `json:"effect_key"`
	Template   string         `json:"template"`
	Recipients []string       `json:"recipients"`
	Context    map[string]any `json:"context"`
}

// Enqueue participates in the producer's transaction, including its rollback.
// Recipients can be bare addresses or declared group/<name> references.
func Enqueue(ctx context.Context, tx pgx.Tx, cfg config.Comms, in Input) (string, error) {
	if err := config.ValidateSMTP(cfg.SMTP); err != nil {
		return "", err
	}
	if !validTemplate(in.Template) || strings.TrimSpace(in.EffectKey) == "" || len(in.EffectKey) > 256 || hasControl(in.EffectKey) {
		return "", errors.New("invalid message template or effect key")
	}
	recipients := []string{}
	for _, recipient := range in.Recipients {
		if name, ok := strings.CutPrefix(recipient, "group/"); ok {
			group, exists := cfg.Groups[name]
			if !exists {
				return "", errors.New("message recipient group is undeclared")
			}
			recipients = append(recipients, group.Recipients...)
		} else {
			recipients = append(recipients, recipient)
		}
		if len(recipients) > 100 {
			return "", errors.New("message needs 1-100 recipients")
		}
	}
	if len(recipients) == 0 {
		return "", errors.New("message needs 1-100 recipients")
	}
	for _, recipient := range recipients {
		if !config.ValidEmail(recipient) {
			return "", errors.New("invalid message recipient")
		}
	}
	slices.Sort(recipients)
	in.Recipients = slices.Compact(recipients)
	if in.Context == nil {
		in.Context = map[string]any{}
	}
	data, err := json.Marshal(in.Context)
	if err != nil || len(data) > 256<<10 {
		return "", errors.New("message context must be JSON at most 256 KiB")
	}
	payload, _ := json.Marshal(struct {
		Template, Sender string
		Recipients       []string
		Context          json.RawMessage
	}{in.Template, cfg.SMTP.From, in.Recipients, data})
	hash := sha256.Sum256(payload)
	id := ulid.Make().String()
	err = tx.QueryRow(ctx, `INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(effect_key) DO NOTHING RETURNING id`, id, in.EffectKey, hash[:], in.Template, data, cfg.SMTP.From, in.Recipients).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM ops.outbox WHERE effect_key=$1 AND payload_hash=$2`, in.EffectKey, hash[:]).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrEffectConflict
		}
	}
	return id, dbError(err)
}

type Message struct {
	ID               string     `json:"id"`
	EffectKey        string     `json:"effect_key"`
	Template         string     `json:"template"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	AvailableAt      time.Time  `json:"available_at"`
	RenderedAt       *time.Time `json:"rendered_at"`
	Attempts         int        `json:"attempts"`
	MaxAttempts      int        `json:"max_attempts"`
	FinishedAt       *time.Time `json:"finished_at"`
	Error            *string    `json:"error"`
	ContentExpiredAt *time.Time `json:"content_expired_at,omitempty"`
}

const messageColumns = `id,effect_key,template,status,created_at,available_at,rendered_at,attempts,max_attempts,finished_at,last_error,content_expired_at`

func List(ctx context.Context, pool *pgxpool.Pool) ([]Message, error) {
	rows, err := pool.Query(ctx, `SELECT `+messageColumns+` FROM ops.outbox ORDER BY created_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, dbError(err)
	}
	messages, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
	return messages, dbError(err)
}

type Delivery struct {
	ID         string     `json:"id"`
	Number     int        `json:"number"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Status     string     `json:"status"`
	Error      *string    `json:"error"`
}
type Detail struct {
	Message    Message    `json:"message"`
	Deliveries []Delivery `json:"deliveries"`
}

func Show(ctx context.Context, pool *pgxpool.Pool, ref string) (Detail, error) {
	id, err := messageID(ref)
	if err != nil {
		return Detail{}, err
	}
	rows, err := pool.Query(ctx, `SELECT `+messageColumns+` FROM ops.outbox WHERE id=$1`, id)
	if err != nil {
		return Detail{}, dbError(err)
	}
	message, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Message])
	if err != nil {
		return Detail{}, dbError(err)
	}
	rows, err = pool.Query(ctx, `SELECT id,number,started_at,finished_at,status,error FROM ops.deliveries WHERE message_id=$1 ORDER BY number DESC LIMIT 100`, id)
	if err != nil {
		return Detail{}, dbError(err)
	}
	deliveries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Delivery])
	return Detail{message, deliveries}, dbError(err)
}

func Retry(ctx context.Context, pool *pgxpool.Pool, ref string) error {
	id, err := messageID(ref)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback(ctx)
	var expiredAt *time.Time
	if err = tx.QueryRow(ctx, `SELECT content_expired_at FROM ops.outbox WHERE id=$1 AND status='failed' FOR UPDATE`, id).Scan(&expiredAt); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("only a failed message can be retried")
	} else if err != nil {
		return dbError(err)
	} else if expiredAt != nil {
		return fmt.Errorf("%w: message content has expired", audit.ErrRefused)
	}
	tag, err := tx.Exec(ctx, `UPDATE ops.outbox SET status='pending',available_at=clock_timestamp(),finished_at=NULL,last_error=NULL,max_attempts=attempts+5 WHERE id=$1 AND status='failed' AND content_expired_at IS NULL`, id)
	if err != nil {
		return dbError(err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("only a failed message can be retried")
	}
	if err = audit.Record(ctx, tx, "comms.retry", "message/"+id, map[string]string{"status": "pending"}); err != nil {
		return err
	}
	return dbError(tx.Commit(ctx))
}

// Resend copies the persisted content and requires a new caller-chosen effect key.
// The CLI requires explicit confirmation before invoking this operation.
func Resend(ctx context.Context, pool *pgxpool.Pool, ref, effect string) (string, error) {
	id, err := messageID(ref)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(effect) == "" || len(effect) > 256 || hasControl(effect) {
		return "", errors.New("new message effect key required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", dbError(err)
	}
	defer tx.Rollback(ctx)
	var originalEffect string
	var expiredAt *time.Time
	if err = tx.QueryRow(ctx, `SELECT effect_key,content_expired_at FROM ops.outbox WHERE id=$1 AND status='delivered' FOR UPDATE`, id).Scan(&originalEffect, &expiredAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("only a delivered message can be resent")
		}
		return "", dbError(err)
	}
	if expiredAt != nil {
		return "", fmt.Errorf("%w: message content has expired", audit.ErrRefused)
	}
	if originalEffect == effect {
		return "", errors.New("resend requires a new effect key")
	}
	newID := ulid.Make().String()
	err = tx.QueryRow(ctx, `INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients,rendered_at,subject,text_body,html_body) SELECT $2,$3,payload_hash,template,context,sender,recipients,rendered_at,subject,text_body,html_body FROM ops.outbox WHERE id=$1 ON CONFLICT(effect_key) DO NOTHING RETURNING id`, id, newID, effect).Scan(&newID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT copy.id FROM ops.outbox copy JOIN ops.outbox original ON original.id=$1 WHERE copy.effect_key=$2 AND copy.payload_hash=original.payload_hash AND copy.subject IS NOT DISTINCT FROM original.subject AND copy.text_body IS NOT DISTINCT FROM original.text_body AND copy.html_body IS NOT DISTINCT FROM original.html_body`, id, effect).Scan(&newID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrEffectConflict
		}
	}
	if err != nil {
		return "", dbError(err)
	}
	if err = audit.Record(ctx, tx, "comms.resend", "message/"+id, map[string]string{"created": "message/" + newID}); err != nil {
		return "", err
	}
	return newID, dbError(tx.Commit(ctx))
}

func messageID(ref string) (string, error) {
	id, ok := strings.CutPrefix(ref, "message/")
	if _, err := ulid.ParseStrict(id); !ok || err != nil {
		return "", errors.New("expected message/<ULID>")
	}
	return id, nil
}
func dbError(err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return audit.ErrRefused
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("message not found")
	}
	return errors.New("communications database operation failed")
}
