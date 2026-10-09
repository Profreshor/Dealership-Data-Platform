package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// Evaluate serializes observation and recording, including manual invocations.
func Evaluate(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string) ([]Evaluation, error) {
	if pool == nil || cfg == nil {
		return nil, errors.New("health requires database and config")
	}
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, healthError(err)
	}
	defer func() {
		finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(finish)
	}()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('ddp:health',0))`).Scan(&locked); err != nil {
		return nil, healthError(err)
	}
	if !locked {
		return nil, errors.New("health evaluation is already running")
	}
	var now time.Time
	if err := conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, healthError(err)
	}
	observations := Client(ctx, pool, cfg, root, now)
	observations = append(observations, jobChecks(ctx, pool, cfg)...)
	observations = append(observations, Platform(ctx, pool, root, cfg)...)
	// Preserve completed observations even when the workload deadline expires.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result := make([]Evaluation, 0, len(observations))
	for _, observation := range observations {
		evaluation, err := record(finish, conn, observation, now)
		if err != nil {
			return result, err
		}
		result = append(result, evaluation)
	}
	if err := queueAlerts(finish, conn, cfg.Comms); err != nil {
		return result, err
	}
	return result, ctx.Err()
}

func record(ctx context.Context, conn *pgx.Conn, observation Observation, now time.Time) (Evaluation, error) {
	evaluation := Evaluation{ID: ulid.Make().String(), ObservedAt: now, Observation: observation}
	raw, err := json.Marshal(observation)
	if err != nil {
		return evaluation, err
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity) VALUES($1,$2,$3,$4,$5,$6)`, evaluation.ID, observation.Ref, now, raw, observation.State, observation.Severity); err != nil {
			return err
		}
		var previous string
		var incident *string
		var lastAlert *time.Time
		var recipients []string
		err := tx.QueryRow(ctx, `SELECT state,incident_id,last_alert_at,recipients FROM ops.alert_state WHERE rule_ref=$1 FOR UPDATE`, observation.Ref).Scan(&previous, &incident, &lastAlert, &recipients)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		kind := ""
		route := observation.Notify
		if observation.State == "unknown" || strings.HasPrefix(observation.Ref, "ddp:") {
			route = []string{"group/platform_ops"}
		}
		if observation.State != "ok" {
			if incident == nil {
				id := ulid.Make().String()
				incident = &id
			}
			if previous != observation.State {
				kind = "alert"
			} else if observation.Severity == "critical" && lastAlert != nil && now.Sub(*lastAlert) >= 24*time.Hour {
				kind = "reminder"
			}
			if kind != "" {
				recipients = append(recipients, route...)
				slices.Sort(recipients)
				recipients = slices.Compact(recipients)
			}
		} else if previous != "" && previous != "ok" {
			kind = "recovery"
			route = recipients
		}
		if kind != "" {
			message := observation.Message
			if kind == "recovery" {
				message = "The check recovered."
			}
			values := map[string]any{"Severity": observation.Severity, "Title": kind + ": " + clip(observation.Ref, 100), "Message": message, "OccurredAt": now.UTC().Format(time.RFC3339), "Details": clip(observation.Target+" "+observation.ExecutionID, 512)}
			payload, _ := json.Marshal(values)
			if _, err := tx.Exec(ctx, `INSERT INTO ops.alerts(id,rule_ref,evaluation_id,incident_id,kind,created_at,recipients,context) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ulid.Make().String(), observation.Ref, evaluation.ID, incident, kind, now, route, payload); err != nil {
				return err
			}
			lastAlert = &now
		}
		if observation.State == "ok" {
			incident = nil
			lastAlert = nil
			recipients = []string{}
		}
		_, err = tx.Exec(ctx, `INSERT INTO ops.alert_state(rule_ref,state,evaluation_id,incident_id,last_alert_at,recipients) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(rule_ref) DO UPDATE SET state=EXCLUDED.state,evaluation_id=EXCLUDED.evaluation_id,incident_id=EXCLUDED.incident_id,last_alert_at=EXCLUDED.last_alert_at,recipients=EXCLUDED.recipients`, observation.Ref, observation.State, evaluation.ID, incident, lastAlert, recipients)
		return err
	})
	return evaluation, healthError(err)
}

// Alert retains routing and template context until the outbox accepts it.
type Alert struct {
	ID           string    `json:"id"`
	Ref          string    `json:"ref"`
	EvaluationID string    `json:"evaluation_id"`
	IncidentID   string    `json:"incident_id"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"created_at"`
	Recipients   []string  `json:"recipients"`
	MessageID    *string   `json:"message_id"`
	Error        *string   `json:"notification_error"`
}

func queueAlerts(ctx context.Context, conn *pgx.Conn, cfg config.Comms) error {
	rows, err := conn.Query(ctx, `SELECT id,recipients,context FROM ops.alerts WHERE message_id IS NULL ORDER BY created_at,id LIMIT 100`)
	if err != nil {
		return healthError(err)
	}
	type pending struct {
		id         string
		recipients []string
		values     map[string]any
	}
	waiting := []pending{}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.recipients, &p.values); err != nil {
			rows.Close()
			return healthError(err)
		}
		waiting = append(waiting, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return healthError(err)
	}
	for _, p := range waiting {
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			nested, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			id, sendErr := comms.Enqueue(ctx, nested, cfg, comms.Input{EffectKey: "health/" + p.id, Template: "alert", Recipients: p.recipients, Context: p.values})
			if sendErr != nil {
				if err := nested.Rollback(ctx); err != nil {
					return err
				}
				// Configuration failures remain visible and are retried next evaluation.
				_, err = tx.Exec(ctx, `UPDATE ops.alerts SET notification_error=$2 WHERE id=$1`, p.id, sendErr.Error())
				return err
			}
			if err := nested.Commit(ctx); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE ops.alerts SET message_id=$2,notification_error=NULL WHERE id=$1`, p.id, id)
			return err
		})
		if err != nil {
			return healthError(err)
		}
	}
	return nil
}

func Latest(ctx context.Context, pool *pgxpool.Pool) ([]Evaluation, error) {
	rows, err := pool.Query(ctx, `SELECT e.id,e.observed_at,e.observation FROM ops.alert_state s JOIN ops.health_evaluations e ON e.id=s.evaluation_id ORDER BY s.rule_ref`)
	if err != nil {
		return nil, healthError(err)
	}
	defer rows.Close()
	result := []Evaluation{}
	for rows.Next() {
		var e Evaluation
		if err := rows.Scan(&e.ID, &e.ObservedAt, &e.Observation); err != nil {
			return nil, healthError(err)
		}
		result = append(result, e)
	}
	return result, healthError(rows.Err())
}

func Alerts(ctx context.Context, pool *pgxpool.Pool) ([]Alert, error) {
	rows, err := pool.Query(ctx, `SELECT id,rule_ref,evaluation_id,incident_id,kind,created_at,recipients,message_id,notification_error FROM ops.alerts ORDER BY created_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, healthError(err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Alert])
	return result, healthError(err)
}
func clip(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.ToValidUTF8(value, "")
}
func healthError(err error) error {
	if err == nil {
		return nil
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		if state.SQLState() == "42501" {
			return fmt.Errorf("%w: health database role lacks permission", audit.ErrRefused)
		}
		return fmt.Errorf("health: postgres %s", state.SQLState())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("health database operation failed")
}
