package metrics

import (
	"context"
	"slices"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

const undeclaredJob = "undeclared"

type databaseCollector struct {
	pool     *pgxpool.Pool
	cfg      *config.Config
	up       *prometheus.Desc
	exec     *prometheus.Desc
	dur      *prometheus.Desc
	health   *prometheus.Desc
	observed *prometheus.Desc
	outbox   *prometheus.Desc
	lag      *prometheus.Desc
}

func NewDatabase(pool *pgxpool.Pool, cfg *config.Config) prometheus.Collector {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &databaseCollector{
		pool: pool, cfg: cfg,
		up:       prometheus.NewDesc("ddp_metrics_database_up", "Whether the database metrics snapshot succeeded.", nil, nil),
		exec:     prometheus.NewDesc("ddp_job_executions_total", "Completed job executions.", []string{"job", "status"}, nil),
		dur:      prometheus.NewDesc("ddp_job_duration_seconds", "Wall duration of completed job executions.", []string{"job", "status"}, nil),
		health:   prometheus.NewDesc("ddp_health_state", "Persisted health state, one-hot by check and state.", []string{"check", "state"}, nil),
		observed: prometheus.NewDesc("ddp_health_observed_timestamp_seconds", "Timestamp of the persisted health observation.", []string{"check"}, nil),
		outbox:   prometheus.NewDesc("ddp_outbox_depth", "Retained outbox rows by status.", []string{"status"}, nil),
		lag:      prometheus.NewDesc("ddp_scheduler_lag_seconds", "Age of the oldest due queued dispatch.", nil, nil),
	}
}

type dbSnapshot struct {
	executions []executionMetric
	health     map[string]healthMetric
	outbox     map[string]float64
	lag        float64
	deployment bool
}
type executionMetric struct {
	job, status          string
	count, durationCount uint64
	sum                  float64
}
type healthMetric struct {
	state    string
	observed time.Time
}

func (c *databaseCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.exec
	ch <- c.dur
	ch <- c.health
	ch <- c.observed
	ch <- c.outbox
	ch <- c.lag
}

func (c *databaseCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '2s'")
	var snap dbSnapshot
	if err == nil {
		snap, err = c.readSnapshot(ctx, tx)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1)
	for _, m := range snap.executions {
		ch <- prometheus.MustNewConstMetric(c.exec, prometheus.CounterValue, float64(m.count), m.job, m.status)
		if m.durationCount > 0 {
			ch <- prometheus.MustNewConstSummary(c.dur, m.durationCount, m.sum, nil, m.job, m.status)
		}
	}
	checks := c.checks(snap.deployment)
	for _, check := range checks {
		m := snap.health[check]
		if m.state == "" {
			m.state = "unknown"
		}
		for _, state := range []string{"ok", "failing", "unknown"} {
			v := float64(0)
			if state == m.state {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(c.health, prometheus.GaugeValue, v, check, state)
		}
		observed := float64(0)
		if !m.observed.IsZero() {
			observed = float64(m.observed.Unix()) + float64(m.observed.Nanosecond())/1e9
		}
		ch <- prometheus.MustNewConstMetric(c.observed, prometheus.GaugeValue, observed, check)
	}
	for _, status := range []string{"pending", "delivering", "delivered", "failed"} {
		ch <- prometheus.MustNewConstMetric(c.outbox, prometheus.GaugeValue, snap.outbox[status], status)
	}
	ch <- prometheus.MustNewConstMetric(c.lag, prometheus.GaugeValue, snap.lag)
}

func (c *databaseCollector) checks(deployment bool) []string {
	checks := []string{"ddp:database", "ddp:scheduler", "ddp:disk", "ddp:outbox"}
	if deployment {
		checks = append(checks, "ddp:deployment")
	}
	if c.cfg == nil {
		return checks
	}
	for name := range c.cfg.Health {
		checks = append(checks, "health/"+name)
	}
	if c.cfg.Deploy.Backup != nil {
		checks = append(checks, jobs.BackupRef)
	}
	for ref := range jobs.Definitions(c.cfg) {
		if ref != jobs.HealthRef && (ref != jobs.CommsRelayRef || c.cfg.Comms.SMTP != nil) && (ref != jobs.BackupRef || c.cfg.Deploy.Backup != nil) {
			checks = append(checks, "ddp:job/"+ref)
		}
	}
	return checks
}

func (c *databaseCollector) readSnapshot(ctx context.Context, tx pgx.Tx) (dbSnapshot, error) {
	s := dbSnapshot{health: map[string]healthMetric{}, outbox: map[string]float64{}}
	// ponytail: scans retained execution/outbox metadata; add rollups when history makes this slow.
	declared := map[string]config.Job{}
	if c.cfg != nil {
		declared = jobs.Definitions(c.cfg)
	}
	refs := make([]string, 0, len(declared))
	for ref := range declared {
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	rows, err := tx.Query(ctx, `SELECT CASE WHEN job_ref=ANY($1::text[]) THEN job_ref ELSE 'undeclared' END,status,count(*),count(*) FILTER (WHERE finished_at IS NOT NULL AND started_at IS NOT NULL AND finished_at>=started_at),COALESCE(sum(EXTRACT(EPOCH FROM (finished_at-started_at))) FILTER (WHERE finished_at IS NOT NULL AND started_at IS NOT NULL AND finished_at>=started_at),0) FROM ops.executions WHERE status IN ('succeeded','failed','interrupted','skipped') GROUP BY 1,status`, refs)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var ref, status string
		var count, durationCount int64
		var sum float64
		if err = rows.Scan(&ref, &status, &count, &durationCount, &sum); err != nil {
			rows.Close()
			return s, err
		}
		s.executions = append(s.executions, executionMetric{ref, status, uint64(count), uint64(durationCount), sum})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ops.deployments)`).Scan(&s.deployment); err != nil {
		return s, err
	}
	rows, err = tx.Query(ctx, `SELECT s.rule_ref,e.state,e.observed_at FROM ops.alert_state s JOIN ops.health_evaluations e ON e.id=s.evaluation_id WHERE s.rule_ref=ANY($1::text[])`, c.checks(s.deployment))
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var ref, state string
		var observed time.Time
		if err = rows.Scan(&ref, &state, &observed); err != nil {
			rows.Close()
			return s, err
		}
		s.health[ref] = healthMetric{state, observed}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT status,count(*) FROM ops.outbox GROUP BY status`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var status string
		var n int64
		if err = rows.Scan(&status, &n); err != nil {
			rows.Close()
			return s, err
		}
		s.outbox[status] = float64(n)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return s, err
	}
	rows.Close()
	var lag float64
	err = tx.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM (clock_timestamp()-available_at)) FROM ops.executions WHERE dispatch='scheduler' AND status='queued' AND available_at<=clock_timestamp() ORDER BY available_at,id LIMIT 1`).Scan(&lag)
	if err == pgx.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if lag > 0 {
		s.lag = lag
	}
	return s, nil
}
