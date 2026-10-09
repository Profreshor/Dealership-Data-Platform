package inspect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RunEvidence struct {
	Run
	LastAttempt *AttemptLog `json:"last_attempt,omitempty"`
}
type Finding struct {
	Code        string `json:"code"`
	Ref         string `json:"ref"`
	ExecutionID string `json:"execution_id,omitempty"`
	Message     string `json:"message"`
}
type Diagnosis struct {
	Ref        string    `json:"ref"`
	RootRef    string    `json:"root_ref"`
	Scope      string    `json:"scope"`
	ObservedAt time.Time `json:"observed_at"`
	State      string    `json:"state"`
	Declared   bool      `json:"declared"`
	Lineage
	IntegrationRefs []string            `json:"integration_refs"`
	AffectedOutputs []string            `json:"affected_outputs"`
	Execution       *RunEvidence        `json:"execution,omitempty"`
	Failures        []RunEvidence       `json:"failures"`
	TotalFailures   int                 `json:"total_failures"`
	Health          []health.Evaluation `json:"health"`
	Findings        []Finding           `json:"findings"`
	Notes           []string            `json:"notes"`
}

// Diagnose joins current declarations to a read-only snapshot of recorded evidence.
// Declared data flow identifies possible impact; it does not prove a root cause.
func Diagnose(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string) (Diagnosis, error) {
	d := Diagnosis{Ref: ref, RootRef: ref, Scope: "current", State: "unknown", IntegrationRefs: []string{}, AffectedOutputs: []string{}, Failures: []RunEvidence{}, Health: []health.Evaluation{}, Findings: []Finding{}, Notes: []string{"Relationships describe the current registry, not a historical configuration snapshot."}}
	if pool == nil || cfg == nil {
		return d, errors.New("diagnosis requires database and config")
	}
	reg, err := registry.Build(cfg)
	if err != nil {
		return d, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return d, queryError(err)
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&d.ObservedAt); err != nil {
		return d, queryError(err)
	}
	executionID := ""
	if strings.HasPrefix(ref, "execution/") {
		if err := validateExecutionRef(ref); err != nil {
			return d, err
		}
		executionID = strings.TrimPrefix(ref, "execution/")
		if err := tx.QueryRow(ctx, `SELECT job_ref FROM ops.executions WHERE id=$1`, executionID).Scan(&d.RootRef); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return d, fmt.Errorf("execution not found")
			}
			return d, queryError(err)
		}
		d.Scope = "execution"
		d.Notes = append(d.Notes, "Health observations are current records, not reconstructed health at the execution time.")
	}
	_, declErr := reg.Get(d.RootRef)
	_, systemJob := jobs.Definitions(cfg)[d.RootRef]
	d.Declared = declErr == nil || systemJob
	if !d.Declared && executionID == "" {
		if err := validateInspectionRef(d.RootRef); err != nil {
			return d, fmt.Errorf("resource not found")
		}
	}
	d.Lineage = ResourceLineage(reg, d.RootRef)
	related := append(append([]string{d.RootRef}, d.Upstream...), d.Downstream...)
	for _, r := range related {
		if strings.HasPrefix(r, "integration/") {
			d.IntegrationRefs = append(d.IntegrationRefs, r)
		}
	}
	slices.Sort(d.IntegrationRefs)
	d.IntegrationRefs = slices.Compact(d.IntegrationRefs)
	candidates := append([]string{d.RootRef}, d.Upstream...)
	if strings.HasPrefix(d.RootRef, "integration/") {
		candidates = append(candidates, d.Downstream...)
	}
	jobRefs := []string{}
	defs := jobs.Definitions(cfg)
	for _, candidate := range candidates {
		if _, ok := defs[candidate]; ok || (candidate == d.RootRef && !d.Declared) {
			jobRefs = append(jobRefs, candidate)
		}
	}
	slices.Sort(jobRefs)
	jobRefs = slices.Compact(jobRefs)
	evidence, total, completed, err := diagnosticRuns(ctx, tx, jobRefs, executionID)
	if err != nil {
		return d, err
	}
	if !d.Declared && executionID == "" && completed == 0 {
		return d, fmt.Errorf("resource not found")
	}
	if !d.Declared {
		d.Notes = append(d.Notes, "The job is absent from the current registry; its historical inputs and outputs are unavailable.")
	}
	if executionID != "" {
		if len(evidence) != 1 {
			return d, fmt.Errorf("execution not found")
		}
		d.Execution = &evidence[0]
		switch evidence[0].Status {
		case "succeeded":
			d.State = "ok"
		case "failed":
			d.State = "failing"
		default:
			d.State = "unknown"
		}
		if evidence[0].Status == "failed" {
			d.Failures = evidence
			d.TotalFailures = 1
		}
	} else {
		d.Failures = evidence
		d.TotalFailures = total
		if len(jobRefs) > 0 && completed == len(jobRefs) {
			d.State = "ok"
		}
		if total > 0 {
			d.State = "failing"
		}
	}
	for _, failure := range d.Failures {
		d.Findings = append(d.Findings, Finding{Code: "job_failed", Ref: failure.JobRef, ExecutionID: failure.ID, Message: "The recorded execution has final status failed."})
		if def, ok := defs[failure.JobRef]; ok {
			for _, output := range def.Writes {
				d.AffectedOutputs = append(d.AffectedOutputs, output.Target)
			}
			if def.Model != "" {
				d.AffectedOutputs = append(d.AffectedOutputs, def.Model)
			}
		}
	}
	slices.Sort(d.AffectedOutputs)
	d.AffectedOutputs = slices.Compact(d.AffectedOutputs)
	if d.TotalFailures > len(d.Failures) {
		d.Notes = append(d.Notes, "Failure evidence is limited to the newest 20 failed jobs; total_failures reports the full count. Affected outputs cover only the displayed failures.")
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.observed_at,e.observation FROM ops.alert_state s JOIN ops.health_evaluations e ON e.id=s.evaluation_id WHERE e.rule_ref=ANY($1) OR e.observation->>'target'=ANY($1) ORDER BY e.rule_ref`, related)
	if err != nil {
		return d, queryError(err)
	}
	for rows.Next() {
		var e health.Evaluation
		if err := rows.Scan(&e.ID, &e.ObservedAt, &e.Observation); err != nil {
			rows.Close()
			return d, queryError(err)
		}
		d.Health = append(d.Health, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return d, queryError(err)
	}
	if d.Scope == "current" {
		currentOK := false
		uncertain := false
		observed := map[string]bool{}
		for _, e := range d.Health {
			observed[e.Ref] = true
		}
		missing := []string{}
		for name, rule := range cfg.Health {
			ruleRef := "health/" + name
			if (slices.Contains(related, ruleRef) || slices.Contains(related, rule.Target)) && !observed[ruleRef] {
				missing = append(missing, ruleRef)
			}
		}
		slices.Sort(missing)
		for _, ref := range missing {
			uncertain = true
			d.Findings = append(d.Findings, Finding{Code: "health_not_observed", Ref: ref, Message: "The declared health rule has no recorded observation."})
		}
		for _, e := range d.Health {
			code := "health_" + e.State
			if d.ObservedAt.Sub(e.ObservedAt) > 3*time.Minute || e.ObservedAt.After(d.ObservedAt) {
				code = "health_stale"
				uncertain = true
			} else {
				switch e.State {
				case "failing":
					d.State = "failing"
				case "unknown":
					uncertain = true
				case "ok":
					currentOK = true
				}
			}
			if code != "health_ok" {
				d.Findings = append(d.Findings, Finding{Code: code, Ref: e.Ref, ExecutionID: e.ExecutionID, Message: "See the recorded health observation and its timestamp."})
			}
		}
		if d.State != "failing" {
			if uncertain || (len(jobRefs) > 0 && completed < len(jobRefs)) {
				d.State = "unknown"
			} else if currentOK {
				d.State = "ok"
			}
		}
	}
	if d.State == "unknown" && len(d.Findings) == 0 {
		d.Findings = append(d.Findings, Finding{Code: "insufficient_evidence", Ref: d.RootRef, Message: "No complete current execution or health evidence is available."})
	}
	return d, nil
}

func diagnosticRuns(ctx context.Context, q queryer, refs []string, id string) ([]RunEvidence, int, int, error) {
	rows, err := q.Query(ctx, `WITH latest AS (
 SELECT DISTINCT ON(job_ref) id,job_ref,dispatch,chain_id,depends_on,reason,cancel_requested_at,scheduled_at,available_at,max_attempts,started_at,finished_at,status
 FROM ops.executions WHERE ($2<>'' AND id=$2) OR ($2='' AND job_ref=ANY($1) AND status IN ('succeeded','failed'))
 ORDER BY job_ref,scheduled_at DESC,id DESC
 ) SELECT id,job_ref,dispatch,chain_id,depends_on,reason,cancel_requested_at,scheduled_at,available_at,max_attempts,started_at,finished_at,status,count(*) OVER(),(SELECT count(*) FROM latest)
 FROM latest WHERE $2<>'' OR status='failed' ORDER BY scheduled_at DESC,id DESC LIMIT 20`, refs, id)
	if err != nil {
		return nil, 0, 0, queryError(err)
	}
	result := []RunEvidence{}
	total, completed := 0, 0
	for rows.Next() {
		var r RunEvidence
		if err := rows.Scan(&r.ID, &r.JobRef, &r.Dispatch, &r.ChainID, &r.DependsOn, &r.Reason, &r.CancelRequestedAt, &r.ScheduledAt, &r.AvailableAt, &r.MaxAttempts, &r.StartedAt, &r.FinishedAt, &r.Status, &total, &completed); err != nil {
			rows.Close()
			return nil, 0, 0, queryError(err)
		}
		result = append(result, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, 0, queryError(err)
	}
	if len(result) == 0 {
		// A successful set has no failure rows to carry the completion count.
		if err := q.QueryRow(ctx, `SELECT count(DISTINCT job_ref) FROM ops.executions WHERE job_ref=ANY($1) AND status IN ('succeeded','failed')`, refs).Scan(&completed); err != nil {
			return nil, 0, 0, queryError(err)
		}
	}
	for i := range result {
		var a AttemptLog
		err := q.QueryRow(ctx, `SELECT id,execution_id,number,started_at,finished_at,status,CASE WHEN payload_expired_at IS NULL THEN right(stdout,4096) ELSE '' END,CASE WHEN payload_expired_at IS NULL THEN right(stderr,4096) ELSE '' END,CASE WHEN payload_expired_at IS NULL THEN left(error,2048) END,payload_expired_at FROM ops.attempts WHERE execution_id=$1 ORDER BY number DESC LIMIT 1`, result[i].ID).Scan(&a.ID, &a.ExecutionID, &a.Number, &a.StartedAt, &a.FinishedAt, &a.Status, &a.Stdout, &a.Stderr, &a.Error, &a.PayloadExpiredAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, 0, queryError(err)
		}
		if err == nil {
			result[i].LastAttempt = &a
		}
	}
	return result, total, completed, nil
}
