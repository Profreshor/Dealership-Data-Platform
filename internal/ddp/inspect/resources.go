package inspect

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/registry"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/scheduler"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RelationColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}
type Relation struct {
	Ref                         string           `json:"ref"`
	Exists                      bool             `json:"exists"`
	Kind                        string           `json:"kind,omitempty"`
	Columns                     []RelationColumn `json:"columns,omitempty"`
	EstimatedRows               int64            `json:"estimated_rows,omitempty"`
	Bytes                       int64            `json:"bytes,omitempty"`
	DirectDependencies          []string         `json:"direct_dependencies,omitempty"`
	LastSuccessfulJobFinishedAt *time.Time       `json:"last_successful_job_finished_at,omitempty"`
}
type Integration struct {
	Ref          string     `json:"ref"`
	Kind         string     `json:"kind"`
	BaseURL      string     `json:"base_url,omitempty"`
	Docs         string     `json:"docs,omitempty"`
	SecretNames  []string   `json:"secret_names,omitempty"`
	Jobs         []string   `json:"jobs,omitempty"`
	LandedTables []string   `json:"landed_tables,omitempty"`
	Relations    []Relation `json:"relations,omitempty"`
}
type HealthExpectation struct {
	Ref      string `json:"ref"`
	Kind     string `json:"kind"`
	Target   string `json:"target,omitempty"`
	MaxAge   string `json:"max_age,omitempty"`
	Severity string `json:"severity,omitempty"`
}
type ModelObservation struct {
	Relation  Relation       `json:"relation"`
	Refreshes []ModelRefresh `json:"refreshes,omitempty"`
}
type ModelRefresh struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
}
type ResourceDetail struct {
	Ref         string              `json:"ref"`
	Type        string              `json:"type"`
	Declaration registry.Resource   `json:"declaration"`
	Job         *Job                `json:"job,omitempty"`
	Execution   *RunDetail          `json:"execution,omitempty"`
	Model       *ModelObservation   `json:"model,omitempty"`
	Table       *Relation           `json:"table,omitempty"`
	Integration *Integration        `json:"integration,omitempty"`
	Health      []HealthExpectation `json:"health,omitempty"`
	Scheduler   *scheduler.State    `json:"scheduler,omitempty"`
}

func ListTables(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) ([]Relation, error) {
	if pool == nil || cfg == nil {
		return []Relation{}, fmt.Errorf("table inspection requires pool and config")
	}
	rows, err := pool.Query(ctx, relationQuery, relationSchemas(cfg))
	if err != nil {
		return []Relation{}, queryError(err)
	}
	defer rows.Close()
	actual := map[string]Relation{}
	for rows.Next() {
		r, e := scanRelation(rows)
		if e != nil {
			return []Relation{}, queryError(e)
		}
		canonicalDependencies(&r, cfg)
		actual[r.Ref] = r
	}
	if err := rows.Err(); err != nil {
		return []Relation{}, queryError(err)
	}
	for name := range cfg.Tables {
		ref := "table/" + name
		if _, ok := actual[ref]; !ok {
			actual[ref] = Relation{Ref: ref}
		}
	}
	for name := range cfg.Models {
		physical := "table/" + name
		if r, ok := actual[physical]; ok {
			delete(actual, physical)
			r.Ref = "model/" + name
			actual[r.Ref] = r
		}
	}
	for name := range cfg.Models {
		ref := "model/" + name
		if _, ok := actual[ref]; !ok {
			actual[ref] = Relation{Ref: ref}
		}
	}
	result := make([]Relation, 0, len(actual))
	for _, r := range actual {
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result, nil
}
func ShowTable(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string) (Relation, error) {
	if pool == nil || cfg == nil {
		return Relation{}, fmt.Errorf("table inspection requires pool and config")
	}
	schema, name, e := relationRef(ref)
	if e != nil {
		return Relation{}, e
	}
	var r Relation
	e = pool.QueryRow(ctx, relationQuery+" AND n.nspname=$2 AND c.relname=$3", relationSchemas(cfg), schema, name).Scan(&r.Ref, &r.Exists, &r.Kind, &r.EstimatedRows, &r.Bytes, &r.Columns, &r.DirectDependencies)
	if e == pgx.ErrNoRows {
		return Relation{Ref: ref}, nil
	}
	if e != nil {
		return Relation{}, queryError(e)
	}
	r.Ref = ref
	canonicalDependencies(&r, cfg)
	return r, nil
}
func ListIntegrations(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) ([]Integration, error) {
	if pool == nil || cfg == nil {
		return []Integration{}, fmt.Errorf("integration inspection requires pool and config")
	}
	r, e := registry.Build(cfg)
	if e != nil {
		return []Integration{}, e
	}
	out := make([]Integration, 0, len(cfg.Integrations))
	for n, d := range cfg.Integrations {
		out = append(out, integrationObservation("integration/"+n, d, r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}
func ShowIntegration(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string) (Integration, error) {
	if pool == nil || cfg == nil {
		return Integration{}, fmt.Errorf("integration inspection requires pool and config")
	}
	n, e := namedRef(ref, "integration")
	if e != nil {
		return Integration{}, e
	}
	d, ok := cfg.Integrations[n]
	if !ok {
		return Integration{}, fmt.Errorf("integration %q not found", ref)
	}
	r, e := registry.Build(cfg)
	if e != nil {
		return Integration{}, e
	}
	out := integrationObservation(ref, d, r)
	for _, t := range out.LandedTables {
		x, e := ShowTable(ctx, pool, cfg, t)
		if e != nil {
			return Integration{}, e
		}
		if d, e := r.Get(t); e == nil {
			x.LastSuccessfulJobFinishedAt, e = latestSuccessful(ctx, pool, d.WrittenBy)
			if e != nil {
				return Integration{}, e
			}
		}
		out.Relations = append(out.Relations, x)
	}
	return out, nil
}

func latestSuccessful(ctx context.Context, pool *pgxpool.Pool, jobs []string) (*time.Time, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	var at *time.Time
	if err := pool.QueryRow(ctx, "SELECT max(finished_at) FROM ops.executions WHERE job_ref=ANY($1) AND status='succeeded' AND finished_at IS NOT NULL", jobs).Scan(&at); err != nil {
		return nil, queryError(err)
	}
	return at, nil
}
func Inspect(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string) (ResourceDetail, error) {
	if pool == nil || cfg == nil {
		return ResourceDetail{}, fmt.Errorf("resource inspection requires pool and config")
	}
	if ref == "service/scheduler" {
		state, err := scheduler.Status(ctx, pool)
		if err != nil {
			return ResourceDetail{}, err
		}
		return ResourceDetail{Ref: ref, Type: "service", Scheduler: &state}, nil
	}
	if jobs.IsSystem(ref) {
		job, err := ShowJob(ctx, pool, cfg, ref)
		if err != nil {
			return ResourceDetail{}, err
		}
		return ResourceDetail{Ref: ref, Type: "job", Job: &job}, nil
	}
	r, e := registry.Build(cfg)
	if e == nil {
		d, x := r.Get(ref)
		if x == nil {
			out := ResourceDetail{Ref: ref, Type: d.Type, Declaration: d, Health: healthFor(cfg, ref)}
			switch d.Type {
			case "job":
				v, x := ShowJob(ctx, pool, cfg, ref)
				if x != nil {
					return out, x
				}
				out.Job = &v
			case "model":
				o := &ModelObservation{}
				if x := modelObservation(ctx, pool, cfg, ref, o); x != nil {
					return out, x
				}
				out.Model = o
			case "table":
				v, x := ShowTable(ctx, pool, cfg, ref)
				if x != nil {
					return out, x
				}
				out.Table = &v
			case "integration":
				v, x := ShowIntegration(ctx, pool, cfg, ref)
				if x != nil {
					return out, x
				}
				out.Integration = &v
			}
			return out, nil
		}
		e = x
	}
	if strings.HasPrefix(ref, "execution/") {
		v, x := ShowRun(ctx, pool, ref)
		if x != nil {
			return ResourceDetail{}, x
		}
		return ResourceDetail{Ref: ref, Type: "execution", Execution: &v}, nil
	}
	return ResourceDetail{}, e
}
func modelObservation(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string, o *ModelObservation) error {
	r, e := ShowTable(ctx, pool, cfg, ref)
	if e != nil {
		return e
	}
	o.Relation = r
	rows, e := pool.Query(ctx, "SELECT started_at,finished_at,status,COALESCE(error,'') FROM ops.model_refreshes WHERE model_ref=$1 ORDER BY started_at DESC LIMIT 10", ref)
	if e != nil {
		return queryError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var x ModelRefresh
		if e := rows.Scan(&x.StartedAt, &x.FinishedAt, &x.Status, &x.Error); e != nil {
			return queryError(e)
		}
		o.Refreshes = append(o.Refreshes, x)
	}
	return rows.Err()
}
func integrationObservation(ref string, d config.Integration, r *registry.Registry) Integration {
	x := Integration{Ref: ref, Kind: d.Kind, BaseURL: d.BaseURL, Docs: d.Docs}
	if d.Auth != nil {
		x.SecretNames = []string{d.Auth.Secret}
	}
	q, _ := r.Get(ref)
	x.Jobs = append(x.Jobs, q.ReadBy...)
	for _, j := range x.Jobs {
		if v, e := r.Get(j); e == nil {
			if job, ok := v.Config.(config.Job); ok {
				for _, w := range job.Writes {
					x.LandedTables = append(x.LandedTables, w.Target)
				}
			}
		}
	}
	slices.Sort(x.LandedTables)
	x.LandedTables = slices.Compact(x.LandedTables)
	sort.Strings(x.Jobs)
	return x
}
func healthFor(cfg *config.Config, ref string) []HealthExpectation {
	out := []HealthExpectation{}
	for n, h := range cfg.Health {
		if h.Target == ref {
			out = append(out, HealthExpectation{Ref: "health/" + n, Kind: h.Kind, Target: h.Target, MaxAge: h.MaxAge, Severity: h.Severity})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}
func namedRef(ref, kind string) (string, error) {
	p, ok := strings.CutPrefix(ref, kind+"/")
	if !ok || p == "" || strings.Contains(p, "/") {
		return "", fmt.Errorf("invalid %s reference %q", kind, ref)
	}
	return p, nil
}
func relationRef(ref string) (string, string, error) {
	p, ok := strings.CutPrefix(ref, "table/")
	if !ok {
		p, ok = strings.CutPrefix(ref, "model/")
	}
	if !ok {
		return "", "", fmt.Errorf("invalid relation reference %q", ref)
	}
	a, b, ok := strings.Cut(p, ".")
	if !ok || a == "" || b == "" || strings.Contains(b, ".") {
		return "", "", fmt.Errorf("invalid relation reference %q", ref)
	}
	return a, b, nil
}

func relationSchemas(cfg *config.Config) []string {
	schemas := append([]string{}, cfg.Database.Layers...)
	for name := range cfg.Tables {
		schema, _, _ := strings.Cut(name, ".")
		schemas = append(schemas, schema)
	}
	for name := range cfg.Models {
		schema, _, _ := strings.Cut(name, ".")
		schemas = append(schemas, schema)
	}
	slices.Sort(schemas)
	return slices.Compact(schemas)
}

func canonicalDependencies(relation *Relation, cfg *config.Config) {
	for i, ref := range relation.DirectDependencies {
		name := strings.TrimPrefix(ref, "table/")
		if _, ok := cfg.Models[name]; ok {
			relation.DirectDependencies[i] = "model/" + name
		}
	}
	slices.Sort(relation.DirectDependencies)
}

const relationQuery = `
SELECT 'table/'||n.nspname||'.'||c.relname, true,
  CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'table'
    WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' ELSE c.relkind::text END,
  GREATEST(c.reltuples,0)::bigint, pg_total_relation_size(c.oid),
  COALESCE((
    SELECT jsonb_agg(jsonb_build_object('name',a.attname,
      'type',format_type(a.atttypid,a.atttypmod),'nullable',NOT a.attnotnull) ORDER BY a.attnum)
    FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
  ),'[]'::jsonb),
  COALESCE((
    SELECT array_agg(DISTINCT format('table/%s.%s',dn.nspname,dc.relname)
      ORDER BY format('table/%s.%s',dn.nspname,dc.relname))
    FROM pg_depend dep JOIN pg_rewrite rw ON rw.oid=dep.objid
    JOIN pg_class dc ON dc.oid=dep.refobjid JOIN pg_namespace dn ON dn.oid=dc.relnamespace
    WHERE rw.ev_class=c.oid AND dep.classid='pg_rewrite'::regclass
      AND dep.refclassid='pg_class'::regclass AND dc.oid<>c.oid AND dc.relkind IN ('r','p','v','m')
  ),ARRAY[]::text[])
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=ANY($1) AND c.relkind IN ('r','p','v','m')`

func scanRelation(rows pgx.Rows) (Relation, error) {
	var r Relation
	var columns []byte
	e := rows.Scan(&r.Ref, &r.Exists, &r.Kind, &r.EstimatedRows, &r.Bytes, &columns, &r.DirectDependencies)
	if e == nil {
		e = json.Unmarshal(columns, &r.Columns)
	}
	return r, e
}
