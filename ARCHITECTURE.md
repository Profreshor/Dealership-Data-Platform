# DDP Architecture

Status: describes the code at the public release, 2026-10-09. Companion to
`VISION.md` (why) and `DECISIONS.md` (what was decided and why). When this document
and a decision disagree, the decision wins and this document gets fixed. When this
document and the code disagree, the code wins.

---

## 1. What DDP is, precisely

- A **template repository**, free and MIT licensed. Each dealership creates its own
  private repo from it with `ddp init`. The dealership's coding agent builds the
  dealership's data system on top of it.
- A complete, opinionated starting point. Every dealership gets the whole platform.
  The dealership's repo adds configuration and business-specific code without
  rebuilding the recurring infrastructure.
- **One Go binary, `ddp`.** It is the CLI, the TUI, the scheduler, the API server,
  the migration runner, the release and backup tool and the agent interface.
- **One small Python package, `ddp`.** It connects bounded Python data jobs to the Go
  runtime: run context, logging, database access, HTTP, landing and results.
- **One TypeScript package, `@ddp/ui`.** The frontend shell: auth, permission guard,
  API client, data tables, forms, layout, theme.
- **One Postgres database** with a fixed schema layout (§6).
- **One Docker image**, run as two services by Docker Compose (§18).
- **One operational truth** (§7): every fact the TUI, the CLI, the JSON output and the
  web console show comes from the same Go packages.

The platform handles the recurring infrastructure. The dealership's own code
supplies what the business needs. DDP standardizes where that work lives, how it
connects, how it runs and how humans and agents inspect it. It does not replace
knowing the business or reviewing changes.

Throughout this document, the **operator** is the person who runs the system for
the dealership: normally someone at the dealership, or an IT provider the dealership
hires, with access the dealership grants and can revoke.

## 2. Design rules

These rules are enforced by `ddp validate`, lint rules, or the compiler wherever
possible.

1. **Everything is text.** Config, schedules, contracts, migrations, models, health
   rules, permissions, templates, deployment and agent instructions.
2. **One operational truth, many interfaces.** No interface computes its own facts.
3. **Every command has `--json`.** Structured output has a stable envelope and
   machine-readable errors. Schemas used by automation have compatibility tests.
4. **YAML describes; code executes.** `ddp.yaml` names what exists, how it connects
   and what DDP must enforce. Go, Python, SQL and TypeScript contain behavior.
5. **Declare each relationship once.** Jobs and models declare what they read and
   write. DDP derives reverse links such as "jobs that write this table."
6. **Package application code and jobs by business capability, never by technical
   layer.** No `extract/`, `transform/`, `handlers/` or `util/` directory trees. SQL
   models keep their deliberate `staging`, `core` and `mart` layout.
7. **Thin, append-never composition root.** Features register; nothing is threaded
   through a growing signature.
8. **Platform never imports dealership code.** `internal/ddp` cannot import
   `internal/app`; `ddp` (Python) cannot import `jobs`; `@ddp/ui` cannot import
   `apps/*`.
9. **Layers have a dependency direction.** `mart` reads `core`, `staging` and landed
   integration data; `core` reads `staging` and landed data; `staging` reads landed
   data. Never upward.
10. **Exit code plus result.** A job succeeds only with exit 0. Its runner always
    writes a structured result, even when the result has no optional metrics.
11. **Freshness measures data, not heartbeats.** A health check reads event time or a
    watermark, never "the job ran".
12. **Business timezone in config; storage in UTC; money is decimal.**
13. **Dangerous actions are gated and audited.** Go command definitions carry their
    risk and confirmation rules. The OS account and database role enforce authority.
14. **Every failable feature has an alert.** No silent failure paths.
15. **No second backend runtime.** Go serves, schedules and operates. Python does data
    work. Nothing else runs long-lived processes.
16. **Temporary seams are named and dated.** Anything marked temporary has a removal
    condition written next to it.
17. **Scaffolds, not `touch`.** New jobs, models, integrations, routes, pages and
    health rules come from `ddp new`, which updates `ddp.yaml` and emits files that
    satisfy the rules.
18. **CI is the wall.** `ddp validate` checks registry completeness and connections.
    A required CI check prevents an invalid project from merging.

## 3. Repository layout

A dealership repo is created from the template. `P` marks platform code that came
from DDP and `D` marks the dealership's own code. The `client/` directory holds the
dealership's own code (its shared Python, by integration), and "client routes" are
the dealership's own Go routes. These labels record provenance,
not law. A dealership can change any source file when its needs require it; changed
platform paths may need manual work during a later template upgrade.

```text
acme/
├── ddp.yaml                  D  the complete project registry
├── cmd/ddp/main.go           P  composition root, append-never
├── internal/
│   ├── ddp/                  P  the platform (Go)
│   │   ├── cli/  tui/  config/  registry/  inspect/  query/  scheduler/  schedule/
│   │   ├── jobs/  models/  endpoints/  serving/  web/  auth/  comms/  health/
│   │   ├── metrics/  migrate/  audit/  backup/  deploy/  doctor/  cleanup/
│   │   ├── onboard/  scaffold/  smoke/  dev/  service/  platform/  buildinfo/
│   │   └── httpx/  render/
│   └── app/                  D  dealership Go features, package by feature
│       └── register.go          app.Register(reg) is the only hook main calls
├── ddp/                      P  the thin Python job interface
├── client/                   D  the dealership's shared Python, by integration
├── jobs/                     D  flat until business capabilities justify folders
├── models/                   D  SQL models, one relation per file (ddp new model)
│   └── staging/  core/  mart/
├── health/                   D  SQL for health rules (ddp new health)
├── migrations/
│   ├── ddp/                  P  platform migrations
│   └── app/                  D  dealership migrations
├── templates/comms/          D  email templates; DDP ships defaults
├── frontend/
│   ├── packages/ddp-ui/      P  the shell
│   └── apps/portal/          D  the dealership's portal application
├── deploy/                   P  Dockerfile, Compose files, systemd units
├── bin/                      P  install.sh, update.sh, verify-backup.sh, publish-image.sh
├── schema/                   P  generated JSON Schema for ddp.yaml
├── skills/                   P+D canonical action procedures for agents
├── docs/                     P+D platform guides, discovery, onboarding plan
├── tests/                    P+D cross-language and system checks
├── .agents/skills/           P  link to canonical skills/
├── .claude/skills/           P  link to canonical skills/
├── .devcontainer/            P  standard development container
├── .github/                  P  required CI workflow and branch policy
├── AGENTS.md                 P  shared engineering guide (Claude, Codex, humans)
├── CLAUDE.md                 P  one instruction: read @AGENTS.md
├── README.md  .env.example  go.mod  pyproject.toml  Makefile
```

`models/` and `health/` appear when the first model or SQL health rule is scaffolded.
`ddp init` writes `.env.example`, `docs/discovery.yaml`, an onboarding plan under
`docs/`, a landing-schema migration and one `client/<system>.py` module per discovered system.

**Upgrade mechanics.** DDP releases are tagged revisions of the public repository.
A dealership upgrade fetches and merges a tagged revision in a dedicated PR, keeps
the dealership's registry facts, source and applied migrations, runs the full checks
and receives human review (`skills/ddp-upgrade`). Git is the upgrade engine; v1 has
no custom package manager or upgrade command. A project that changes platform code
accepts the related merge work. Tags are human-readable release names, not a
semantic-version compatibility promise. `ddp doctor` reports drift between local
platform files and the recorded `ddp.template_revision`.

A platform defect found in a dealership repo is best reproduced and fixed upstream
first, then merged back through a tagged release. Useful dealership code moves
upstream only when it removes recurring work for other dealerships and can be
proven with synthetic data.

**Why the platform is copied, not imported.** Dealerships may need Go. A Go feature
in `internal/app` imports `internal/ddp` directly, gets compile-time checks against
the registry, and ships in the same binary. That requires the source in the repo.
The source is also the final escape hatch when configuration is not enough.

## 4. Runtime topology

```text
  browser ─────────► Cloudflare Tunnel ──────────────► cloudflared ──► api (127.0.0.1:8080)
  administrator ───► Cloudflare Access (MFA) + Tunnel ► cloudflared ──► sshd (loopback only)

  api ───────┐
             ├──► postgres: ddp ops app <integration>… staging core mart
  scheduler ─┘    scheduler owns jobs, models, health, comms, backup, cleanup
```

Two long-lived processes, one image. `api` is stateless. `scheduler` is the single
worker process: it owns the job runner, model refreshes, health evaluation, the comms
relay, backups and cleanup. Both expose bounded Prometheus metrics on loopback, but
v1 does not ship Prometheus or Grafana. `ddp dev` runs the API against a
Compose-managed development Postgres, with Vite proxying `/api`; `--scheduler` adds
the scheduler loop.

CLI commands run against the same Postgres. On a deployed host, the dealership's
administrator runs them through Compose: `docker compose run` against the
`maintenance` service with the credential the task needs, or `exec` into the `api`
container. Read-only investigation uses the `ddp_readonly` login, so PostgreSQL
itself refuses writes (`skills/ddp-investigate`). The only interactive host account
is the existing non-root administrator the dealership names at installation; DDP
creates no other accounts.

## 5. Configuration

`ddp.yaml` is the project registry. It is the first place a human or agent looks to
see what the dealership's project contains and how the parts connect. It starts as
one file. DDP will add includes only when a real project proves they are needed.

Every supported top-level section is present, even when empty. `{}` means the project
supports that DDP feature but does not use it. A missing required section means the
registry is incomplete or belongs to an incompatible template version.

```yaml
# ddp.yaml
ddp:
  template_revision: 0123456789abcdef0123456789abcdef01234567
  name: acme
  display_name: Acme Dealership
  timezone: America/Chicago
  branding: { logo: frontend/apps/portal/public/logo.svg, accent: "#2457c5" }

database:
  layers: [staging, core, mart]

scheduler:
  max_workers: 4
  defaults:
    timeout: 1h
    retry: { max_attempts: 2, initial_delay: 1m, max_delay: 15m }
    catchup: latest_only

integrations:
  dms:
    kind: http
    base_url: https://api.dms.example.com/v1
    auth: { type: api_key, header: X-API-Key, secret: DMS_API_KEY }
    docs: https://docs.dms.example.com

jobs:
  sync_dms_customers:
    purpose: Pull customers from the DMS into Postgres.
    action: ingest
    python: jobs.sync_dms_customers
    reads: [integration/dms]
    deletions: tombstone
    writes:
      - { target: table/dms.customers, mode: upsert, key: [id] }
    schedule: "*/15 * * * *"
    timeout: 5m

tables:
  dms.customers:
    purpose: Source-shaped customer records from the DMS.
    contract:
      columns:
        id: { type: text, nullable: false, description: DMS customer identifier. }
        payload: { type: jsonb, nullable: false, description: Original source record. }
        _loaded_at: { type: timestamptz, nullable: false }
        _source_key: { type: text, nullable: false }
      primary_key: [id]

models: {}
health: {}
endpoints: {}
pages: {}
comms: {}
permissions: {}

serving:
  addr: ":8080"
  public_url: https://portal.acmedealership.example
  session_ttl: 8h

deploy:
  image: ghcr.io/acme-dealership/acme
  channel: stable
  edge: cloudflare
```

Rules:

- **Secret values never appear in YAML.** Platform secrets use fixed environment
  names such as `DATABASE_URL`; integrations name their required variables. Go
  resolves them and gives a job only the secrets required by its declared
  integrations. Protected, mode-`0600` environment files on the host (`.env`,
  `.env.api`, `.env.scheduler`) are the only baseline secret store; `ddp doctor`
  checks `.env` metadata and reports missing names without printing values.
- **A JSON Schema is generated from the Go config structs.** `ddp config schema`
  prints it; agents and editors validate against it.
- **Configuration changes use the normal delivery path.** A PR passes validation,
  builds an image and restarts the Go processes during deployment. There is no
  production hot reload and no database-backed configuration-version system.
  Configuration is embedded in the image; the running binary reports its Git
  revision.
- **References use typed names.** Examples are `integration/dms`,
  `job/sync_dms_customers`, `table/dms.customers`, `model/mart.customers`,
  `endpoint/customers` and `page/customers`.
- **Each relationship is written once.** Jobs and models declare what they read and
  write. Integrations and tables do not repeat reverse lists. `ddp inspect` derives
  "read by" and "written by" from the registry.
- **Code paths are implementation details.** A registry name remains stable when a
  Python or SQL file moves.
- **Contracts are minimum-compatible.** Required columns, compatible Postgres types,
  nullability and constraints are enforced. Extra source fields are allowed.
- **DDP defines its standard columns once.** Dealership-defined contract columns
  include a short definition. Projects do not repeat the definitions of `_loaded_at`
  and `_source_key`.
- **`ddp validate` enforces registry completeness.** It checks required sections and
  fields, typed references, entrypoints, contracts, layer direction and DAG cycles.
  CI runs it as a required check. Other CI stages run language tests, integration
  tests and secret scanning.
- **The author keeps declarations honest.** When code changes a job's purpose,
  inputs, outputs, schedule or contract, the same PR updates `ddp.yaml`. CI can
  validate declared facts but cannot infer every semantic change in arbitrary Python.
  Agent procedures and human review enforce this due diligence; DDP does not trace
  SQL or create per-job database grants to guess it.

## 6. Postgres layout

One database. Schemas by role:

| Schema | Layer | Owner | Contents |
|---|---|---|---|
| `ddp` | platform | ddp | migration ledgers and privileged-action audit |
| `ops` | platform | ddp | scheduler ticks, executions, attempts and events; health results; alerts; outbox and delivery log; watermarks; model refreshes; backup and deployment evidence |
| `app` | platform | ddp | users, sessions, roles, permissions, user audit; dealership app tables by convention |
| `<integration>` | landing | dealership | one schema per input integration; source-shaped rows with `_loaded_at` and `_source_key` |
| `staging` | transform | dealership | typed and cleaned relations, one per source entity |
| `core` | transform | dealership | integrated business entities |
| `mart` | transform | dealership | consumer-facing relations served by endpoints and pages |

Roles:

| Role | Used by | Grants |
|---|---|---|
| `ddp_owner` | deployment migrations only | owns schemas and migration-created objects; credentials absent from running services |
| `ddp_scheduler` | scheduler and model refreshes | read-write `ops`; refresh declared model relations |
| `ddp_backup` | scheduler-owned backups, through a separate credential | read all data for full archives; write backup evidence and append audits; no dealership-data writes, schema changes or restore authority |
| `ddp_job` | Python jobs | broad DML on dealership data schemas and insert to outbox; no schema or role changes |
| `ddp_api` | `ddp api` | read `mart`, `core` and safe `ops` views; read-write `app` and `ops.outbox` |
| `ddp_readonly` | the dealership's operators and investigators, `ddp doctor` at install | read dealership data and safe operational views; no auth, session, reset-token or message-body tables |

These are `NOLOGIN` permission groups. `ddp provision <component>` creates or
rotates a separate `ddp_<component>_login`, with membership in exactly its group.
Each login defaults to its permission group: migrations use the owner group and
model apply uses the scheduler group.
Initial migrations and login provisioning require cluster administration;
subsequent migrations use the restricted owner credential. See
[host credential setup](docs/deployment.md).

Conventions: `timestamptz` only; `numeric` for money; `text` over `varchar`; `_loaded_at`
on every landed row; primary keys named `id`; ULIDs for platform ids. DDP queries
Postgres system views for the tables, columns, dependencies, row estimates and sizes
that exist now (§7).

## 7. Registry, inspection and operational truth

Everything DDP can inspect has a stable typed reference:

```text
job/sync_dms_customers   execution/01J9…        integration/dms
table/dms.customers      model/mart.customers   endpoint/customers
health/customers_fresh   group/platform_ops     ddp:scheduler
```

`ddp inspect <ref>` combines three plain sources of information:

1. `ddp.yaml` says what should exist and how it should connect.
2. Postgres system views say which schemas, tables, columns and view dependencies
   exist now, along with row estimates and sizes.
3. `ops.*` says what ran, what failed, when data last changed and which health rules
   passed.

DDP computes reverse relationships from the registry. A table does not maintain a
list of readers and writers when jobs, models and endpoints already name their own
inputs and outputs. There is no second persisted catalog and no catalog snapshots.

Every registered item answers the same questions with one JSON shape:

| Field | Source |
|---|---|
| identity, type, config | `ddp.yaml` |
| status | recent runs and declared health rules |
| dependencies, upstream, downstream | typed registry references and derived reverse links |
| recent history | attempts, refreshes and health results |
| health expectations | declared health rules and freshness |
| observed metrics | Postgres and the metrics registry |
| evidence | log tails, last errors, watermarks |
| available actions | Go command metadata filtered by the caller's enforced authority |

`ddp status`, `ddp inspect <ref>`, `ddp diagnose <ref>`, `ddp search <term>`,
the TUI and the web console all use the same Go inspection packages. Git shows
configuration changes; `ops.*` preserves execution history.

## 8. Scheduler

A durable scheduler with structured results and platform system jobs.

**Job definition** (`ddp.yaml`):

```yaml
jobs:
  sync_dms_customers:
    purpose: Pull customers from the DMS API into dms.customers.
    action: ingest                              # ingest | transform | check |
                                                # export | notify | operate | maintain
    python: jobs.sync_dms_customers
    reads: [integration/dms]
    deletions: tombstone                         # ignore | tombstone | reconcile | replace
    writes:
      - { target: table/dms.customers, mode: upsert, key: [id] }
    schedule: "*/15 * * * *"                    # cron in the business timezone
    timeout: 5m
    retry: { max_attempts: 3, initial_delay: 1m, max_delay: 15m }
    catchup: latest_only                         # none | latest_only
    concurrency_key: dms
    tags: [api]

  refresh_customer_mart:
    action: transform
    model: model/mart.customers
    after: [job/sync_dms_customers]
```

V1 schedules Python jobs and SQL model refreshes. Platform system jobs and
dealership Go jobs register directly in Go. There is no arbitrary shell or generic
command runner. Every ingest job declares how the external source represents
deletions; there is no default because the source system determines the correct
behavior.

**Dependencies.** A scheduled job is a root. A job with `after` runs only after its
upstream jobs succeed in the same execution chain. A job cannot have both `schedule`
and `after`. `ddp validate` rejects cycles. A complex multi-source workflow uses an
explicit coordinating job that checks input freshness; DDP does not guess which
unrelated scheduled runs belong together.

**Planning.** The scheduler holds one Postgres advisory lock, so only one instance
plans work. It evaluates five-field cron in the project's business timezone and
stores both the local scheduled time and resolved UTC instant. A nonexistent local
time creates no tick; a repeated local wall-clock time creates one. Missed ticks are
recorded. `none` leaves them skipped; `latest_only` creates one execution for the
newest miss. Historical replay is an explicit backfill.

Every job is non-overlapping by default: its job id is its implicit concurrency key.
An explicit shared key serializes jobs that use the same constrained integration.
Keys live in an in-process map because there is one scheduler. Retries use one fixed
capped exponential-backoff algorithm with jitter; YAML supplies only maximum
attempts, initial delay and maximum delay.

**Execution model.** Due work is saved before launch and handed to a bounded worker
pool. Runs are subprocesses in their own process group with a deadline. On timeout,
DDP sends SIGTERM to the group, waits, then sends SIGKILL. A bounded amount of
stdout and stderr is stored per attempt in Postgres; crossing the limit records a
truncation event. Containers keep no durable run-log files.

On shutdown, the scheduler stops planning and launching, gives active jobs a short
grace period, terminates the remaining process groups and marks those attempts
`interrupted`. Startup reconciles unfinished executions and applies their existing
retry policy.

**Execution and attempt identity.** Each logical run has a stable `execution_id`.
Each process launch has a unique `attempt_id`. Retries keep the same `execution_id`
and create a new `attempt_id`. A rerun creates a new execution. A backfill creates
one execution per historical scheduled occurrence.

**Contract with the workload.** Exit 0 is success. The runner provides a JSON context
file with the execution id, attempt id, scheduled time, business timezone, declared
inputs and outputs, and job settings. It also provides a result-file path. Every
runner writes a JSON result, even when it contains no optional fields. Results may
include rows read, rows written, a watermark, warnings and job-specific details.

**Idempotency.** DDP exposes a stable effect key derived from the `execution_id` and
an effect name. Each `notify`, `export` or `operate` job must declare one strategy:

| Strategy | Meaning |
|---|---|
| `provider_key` | The external system accepts DDP's effect key. |
| `natural_key` | The operation uses a stable business identifier. |
| `reconcile` | The job reads external state before it writes. |
| `duplicates_acceptable` | Duplicate effects are harmless; the registry requires a reason. |

The integration declares the mechanisms it supports; the job selects the strategy
for its operation. `ddp validate` rejects a side-effecting job with no strategy.
Retries use the same effect keys. Rerunning a side-effecting job requires explicit
confirmation and creates new keys. DDP promises at-least-once execution with
idempotent effects where the external system permits it. It does not claim exactly
once behavior across systems it does not control.

**State** lives in `ops.jobs`, `ops.job_overrides`, `ops.ticks`, `ops.executions`,
`ops.attempts`, `ops.events`, `ops.heartbeats` and `ops.planner_state`. Alerting
continuity lives in `ops.alert_state` (§14).

**System jobs** are registered by the platform, tagged `system`, and visible like any
other: `ddp:health` (health evaluation), `ddp:comms_relay`, `ddp:cleanup` (run
history, logs, sessions), `ddp:backup`, and one `model/<name>` refresh job per
scheduled model.

**Operator surface.** `ddp jobs list|show|run|pause|resume|backfill`,
`ddp runs list|show|cancel`, `ddp logs <execution|job>`, `ddp scheduler status`.
`jobs run` runs only the named job unless `--with-downstream` is explicit. Pausing
blocks future work but does not cancel an active attempt or replay skipped ticks on
resume. A paused downstream job stops its chain and records why. Backfills show the
number of executions before starting; side-effecting backfills require confirmation
that repeats the declared idempotency strategy.

## 9. Python job kit (`ddp/`)

A Python job is one bounded dealership operation with one typed entrypoint:

```python
from client.dms import DMSClient
from ddp import JobContext, JobResult, job, landing

@job
def run(ctx: JobContext) -> JobResult:
    dms = DMSClient(ctx.integration("dms"))
    since = ctx.watermark("customers")                       # ops.watermarks
    rows = dms.customers(updated_since=since)
    n = landing.upsert(ctx, "dms", "customers", rows, key="id")
    watermark = max(r["updated_at"] for r in rows) if rows else since
    return JobResult(rows_written=n, watermark=watermark)
```

The Go scheduler expands `python: jobs.sync_dms_customers` to the standard Python
runner. The runner imports `run`, reads the JSON run context produced by Go, catches
uncaught exceptions, writes the result file and sets the exit code. Python never
parses the full DDP registry and never runs a second scheduler, health loop or alert
system.

The small Python package provides:

- **`JobContext`**: execution id, attempt id, scheduled time, business timezone,
  declared inputs and outputs, non-secret job and integration settings, structured
  logging, the current watermark and stable effect keys. Go passes only the job's
  declared secrets through the process environment.
- **Database access** (`ddp.db`): explicit transactions plus small helpers for
  `execute`, `execute_file` and `copy_from`. Python may read and write dealership
  data directly.
- **`landing`**: `upsert`, `replace`, `append` into `<integration>.<entity>` with a JSONB
  `payload`, extracted key columns declared in the registry, `_loaded_at`,
  `_source_key`. Landing raw JSON first and typing in `staging` SQL is the default
  pattern; typed landing tables are declared per entity when the shape is stable.
- **HTTP** (`ddp.http`): explicit timeouts, authentication-header injection, safe
  logging, redirect refusal, response size limits, `Retry-After` support and a few
  quick retries for connection failures, `429` and transient `5xx` responses on
  idempotent methods. Pagination, cursors, response shapes and provider error
  meanings stay in the dealership's own code. Scheduler retries remain authoritative.
- **`JobResult`**: typed, optional counts, watermark, warnings and details.
- **Testing** (`ddp.testing`): a `ddp_db` pytest fixture creates an ephemeral schema
  set. HTTP tests use small hand-written or manually sanitized fixtures; DDP never
  records production responses automatically.

Python jobs never deliver email themselves. The job role may insert into the
outbox; the Go relay delivers (§13).

A watermark advances only after the job transaction commits, the Python process
exits successfully and Go validates its result. A crash between the data commit and
watermark update replays the same source range through the declared idempotent write
mode. Failed replacement jobs preserve the last successful landed data and make its
freshness degrade rather than emptying the table.

Import boundary: `jobs` imports `ddp`; `ddp` never imports `jobs`. Enforced by
import-linter. Pyright runs in strict mode.

Jobs start in one flat `jobs/` directory. A dealership may later group them by
business capability or integration, never by action type. DDP does not enforce
line-count or cyclomatic-complexity thresholds. Split a job when part of it needs
independent scheduling, retries, observability, reuse or testing. Shared dealership
behavior belongs in a specifically named module under `client/`, not copied
helpers, `utils.py` or a premature framework.

## 10. Integrations

An integration is an external system that a project reads from or writes to: the
DMS, CRM, accounting system, OEM portals and the rest. A source is an integration
used as input; DDP does not use `source` as the broader registry term.

```yaml
integrations:
  dms:
    kind: http
    base_url: https://api.dms.example.com/v1
    auth: { type: api_key, header: X-API-Key, secret: DMS_API_KEY }
    docs: https://docs.dms.example.com
    idempotency:
      provider_key: { header: Idempotency-Key }
      natural_key: true
```

Dealership-specific API extraction, scraping and file parsing are bounded Python
jobs. Go owns continuously running or systems-heavy integrations such as CDC,
replication, file watching and leases. DDP adds a shared Go integration interface
only when two real implementations need the same seam.

`ddp integrations list|show` exposes declared settings, landed tables, freshness
and the jobs that use the integration. Relationships are derived from job
declarations; the integration does not repeat them. Proving real access is a
deliberate step during onboarding, not something a reachable hostname implies.

Incremental jobs must handle late or out-of-order updates in their implementation
and tests through an overlap, cursor or reconciliation strategy appropriate to the
external system. DDP does not put a generic cursor algorithm in YAML.

## 11. SQL models

One relation lives in each file under `models/<layer>/<name>.sql`. The file contains
exactly one PostgreSQL `SELECT`; DDP owns creation, replacement, refresh,
transactions and bookkeeping. The registry contains the stable name, purpose,
inputs, materialization and output contract:

```yaml
models:
  mart.customers:
    file: models/mart/customers.sql
    materialization: materialized_view # view | materialized_view
    purpose: One row per customer with lifetime service revenue and last repair order date.
    reads:
      - table/staging.customers
      - table/staging.repair_orders
      - table/staging.invoices
    contract:
      columns:
        id: { type: text, nullable: false, description: Customer identifier. }
        lifetime_value:
          type: numeric
          nullable: false
          description: Invoiced customer revenue before tax.
      unique_key: [id]
```

`ddp models plan` orders models from their declared `reads` and shows what will
change. It does not parse SQL to build a second dependency graph. `ddp models apply`
creates or replaces models in order, one transaction per model, and records the
outcome in `ops.model_refreshes`. A refresh failure rolls back and preserves the last
usable relation; downstream work does not run. A scheduled job refers to the model
by its stable typed name.

CI creates the models in a test database and verifies the output contracts. For
views and materialized views, it also compares dependencies recorded by Postgres
with the registry. Periodic health rules, not every refresh, check semantic claims
such as uniqueness, non-null values, accepted ranges and freshness. A model's
`unique_key` is a business identity used by tests, relationships and pagination; it
does not pretend a view has a physical primary-key constraint.

Migrations create and alter durable tables, constraints and application state.
Ingest jobs populate landed tables. Models define derived reporting relations and
never act as a second migration system. V1 uses live views and cached materialized
views. A genuinely incremental derived table is a stateful job.

Layer rules are validated: a `mart` model may read `core`, `staging` and landed
integration data; `core` may read `staging` and landed data; `staging` may read
landed data. Raw JSON landing accepts extra source fields. Staging casts required
fields into typed columns and fails clearly when an incompatible source change
appears. The raw payload remains available for repair and replay.

SQL tests always run against disposable Postgres with synthetic fixtures. DDP does
not substitute SQLite, mock a SQL engine, gate plans by estimated cost or snapshot
`EXPLAIN`; performance changes follow measured production behavior.

## 12. Serving layer

**Router.** `net/http` with Go 1.22 patterns. Routes register through a registry
that requires a typed policy:

```go
reg.Handle("GET /api/customers", h.list, web.Permission("customers.read"))
reg.Handle("POST /api/admin/users", h.create, web.Admin())
reg.Handle("GET /healthz", health.Live, web.Public())
```

`Build()` fails at startup on nil, duplicate or empty patterns. Policies are only
`Public`, `Authenticated`, `Permission(name)` and `Admin`. Roles grant permissions;
routes do not require roles directly. The edge chain is recover, request id, access
log, security headers, cache control, auth flood guard, then CSRF for cookie
sessions. One `httpx` package owns the envelope
(`{ok, data, error}`), pagination, decoding and error rendering; feature packages do
not write their own.

**Declarative endpoints.** Most read-only reporting endpoints need no Go:

```yaml
endpoints:
  customers:
    reads: [model/mart.customers]
    path: /api/customers
    policy: permission:customers.read
    columns: [id, name, city, last_repair_order_at, lifetime_value]
    filters: [city]
    sort: [name, -last_repair_order_at]
    search: [name]
    unique_key: [id]
    page_size: 50
    export: { format: csv, max_rows: 50000 }
```

A generic handler serves list, get-by-id, filter, sort, search, cursor pagination
and streamed CSV export with whitelisted columns, a query timeout, a row ceiling and
parameterized SQL. Paginated endpoints require a stable unique key, which DDP adds
as the final sort tie-breaker. A one-row aggregate instead declares
`shape: singleton`. Search is case-insensitive matching across explicit columns,
using ordinary Postgres indexes when measurement calls for them. YAML names the
relation and allowed operations; SQL owns joins and calculations. `ddp routes
--json` lists every declarative and Go route with its policy.

**Go features.** `internal/app/<feature>` owns its types, SQL, logic and handlers,
and exposes `Register(reg *web.Registry)`. `internal/app/register.go` calls each one.
`ddp new route` scaffolds both. The composition root in `cmd/ddp` never changes for
a new feature.

**Auth.** V1 has complete invite-only local accounts: email and Argon2id password,
rate limiting, welcome flow and password reset. There is no public registration.
Reset requests return the same response whether an email exists; tokens are random,
stored only as hashes, short-lived and one-use. A successful reset revokes existing
sessions.

Sessions are random opaque cookie values whose hashes live in Postgres. Cookies are
Secure, HttpOnly and SameSite; sessions rotate after login and privilege changes,
and unsafe requests require CSRF protection. Each request resolves the user's roles
and permissions during session lookup. There is no permission snapshot,
`session_version`, HMAC wrapper, Redis seam or auth-provider interface in v1.

`Admin()` is reserved for operator accounts: the first is created by
`ddp users bootstrap` at installation. Dealership managers receive explicit
permissions such as `users.manage`; they do not thereby gain access to job logs,
integrations, releases or the system console. Frontend guards improve the
interface, but server policy is always authoritative.

**Health.** `/healthz` (liveness), `/readyz` (database and migration state), a
private loopback metrics listener, and `/api/system/*`, which serves the operational
truth (§7) to the web console under `Admin()`.

**SPA.** The built frontend is embedded in the binary with `go:embed` and served with
immutable caching for hashed assets. In `ddp dev`, Vite serves it and proxies `/api`.

## 13. Communications

V1 has one Postgres outbox, one Go relay and one delivery method: SMTP. Producers
insert the template name, context, resolved recipients and stable effect key inside
their own transaction. The effect key has a uniqueness constraint, preventing the
same logical execution from enqueueing the same message twice.

The relay claims a pending row, renders it once, persists the exact subject and body,
then sends it. Retries deliver that persisted content; a later template change
does not change an already-attempted message. SMTP acceptance is the only success
DDP can assert. If a provider accepted a message before the acknowledgement was
lost, a retry can duplicate it; SMTP is therefore explicitly classified as
`duplicates_acceptable`.

Templates are under `templates/comms/<name>.{txt,html}`. Plain messages use
`text/template`; HTML uses `html/template`, and missing values are errors. DDP
ships alert, digest, password-reset, password-changed and welcome templates plus
preview and test-send commands. V1 sends links instead of attachments.

Recipient groups avoid repeating addresses:

```yaml
comms:
  smtp:
    addr: smtp.example.test:587
    from: notifications@acmedealership.example
    username: notifications
    password_env: SMTP_PASSWORD
    tls: starttls
  groups:
    platform_ops: { recipients: [it@acmedealership.example] }
    owners: { recipients: [principal@acmedealership.example] }
```

`platform_ops` holds the people the dealership names to receive platform alerts;
`ddp init` refuses discovery without it. Platform failures always notify
`platform_ops`. A business rule must name its group explicitly. Outbox rows move
through `pending`, `delivering`, `delivered` or `failed`; attempts live in
`ops.deliveries`. `comms retry` retries a failed row with the same effect key and
rendered content. Resending an already delivered message creates a new effect and
requires confirmation. Cleanup removes message bodies after the configured retention
period while preserving audit metadata.

## 14. Health, alerts, metrics, logs

**Health rules** are declared in `ddp.yaml`:

```yaml
health:
  customers_fresh:
    kind: freshness             # freshness | sql
    target: table/dms.customers
    column: _loaded_at
    max_age: 2h
    severity: warning
    notify: group/platform_ops
  no_orphan_invoices:
    kind: sql
    sql: health/no_orphan_invoices.sql
    severity: critical
    notify: [group/platform_ops, group/owners]
```

Dealership rules have two kinds. `freshness` compares a declared timestamp or
watermark with `max_age`. A `sql` rule returns exactly one row containing required
boolean `ok` and optional text `message` and `value`; the SQL owns the calculation,
so YAML has no comparison language. Rules use one global evaluation interval unless
a real rule needs its own schedule.

Each evaluation is `ok`, `failing` or `unknown`. `failing` means the check ran and
found a problem. A timeout, invalid query or database error is `unknown` and creates
a technical alert to `platform_ops`. Severity (`warning` or `critical`) is separate
from state. Alerts are sent on a transition into `failing` or `unknown`, once on
recovery, and daily only while an unresolved critical remains. Every evaluation is
retained even when no message is sent.

Job failure, disk capacity, backup status, deployment outcome, scheduler state,
database connectivity and outbox backlog are platform checks implemented in Go.
Platform failures always route to `platform_ops`; other groups receive only
business rules that explicitly name them. Job failures alert after the final retry,
so a recovered retry is not an incident. `ddp:health` records dealership rules and
platform checks in the same operational tables without pretending they are the same
implementation.

**Metrics.** Both processes expose Prometheus metrics on private loopback listeners:
HTTP duration by bounded route pattern, run counts and durations by job and status,
health states, outbox depth, scheduler lag, Go and process collectors. Labels never
contain execution ids, email addresses, URLs, error text or customer ids. DDP does
not ship a Prometheus or Grafana deployment in v1, and metrics are not routed
through the portal hostname. `ddp health`, `ddp doctor` and the TUI use Postgres.
Nothing is sent anywhere.

**Logs.** Go uses `slog` JSON on stdout with `request_id` or `run_id`. Job output is
captured with a per-attempt size limit in Postgres. Logs never include secrets,
authentication headers, session tokens, full integration payloads or arbitrary
database rows by default. `ddp logs <ref>` reads an execution's or job's stored
output from Postgres; service output stays in Docker's logs. `ddp:cleanup` enforces
retention.

**Doctor.** `ddp doctor` calls the same underlying Go checks used by continuous
platform health, then prints concrete repairs. It checks configuration, `.env`
mode and required secret names, Docker, the serving port, template drift, SMTP
reachability, database reachability, scheduler, disk, outbox, migration state and
clock skew. Exit 3 means unhealthy. `/readyz` is narrower: configuration loaded,
migrations current and Postgres reachable. Business-data or integration failures
never make the portal disappear.

## 15. CLI and the agent interface

`ddp` with no arguments and a TTY opens the TUI. Everything else is a command.

| Area | Commands |
|---|---|
| setup | `init`, `doctor`, `dev`, `config validate\|diff\|schema`, `provision` |
| operate | `status`, `jobs`, `runs`, `logs`, `integrations`, `models`, `tables`, `health`, `comms`, `users`, `routes`, `migrate`, `backup`, `deploy plan\|record\|status`, `scheduler status` |
| understand | `inspect <ref>`, `diagnose <ref>`, `search <term>`, `registry`, `sql <query>` |
| build | `new job\|model\|integration\|endpoint\|route\|page\|health\|migration`, `check`, `validate`, `smoke` |
| agent | `capabilities`, `help --json` |
| run | `api`, `scheduler`, `serve --all` |

Rules: `--json` on every command emits a stable envelope with structured errors;
humans get tables through the `render` package. Exit codes are 0 ok, 1 error, 2
usage, 3 unhealthy or drift, and 4 refused. Nothing scrapes human output.

The Go definition that registers a command also states its machine-readable name,
risk, required operating role, confirmation rule and audit behavior. The same record
drives execution, `help --json` and `capabilities --json`; projects do not duplicate
it in YAML. The generic `ddp act` dispatcher does not exist. Real commands own their
validation and safety.

The OS account, database credential or authenticated web session establishes
authority. `DDP_PRINCIPAL` and `--as` do not exist. Privileged deployments,
migrations, user or role changes, manual job operations, communication resends and
write SQL record principal, action, target, time and outcome in `ddp.audit`.

Development agents work on a development computer or in the development container
and submit branches through required, human-reviewed PRs. Production data does not
enter development by default; tests use synthetic or small, manually reviewed and
sanitized fixtures. Production investigation uses the `ddp_readonly` login through
the dealership's own host access (`skills/ddp-investigate`): it may inspect status,
logs and data, then report. The database login is read-only, so PostgreSQL refuses
its writes, and the helper blanks the backup storage credentials and recovery key
the maintenance container would otherwise receive. The helper runs with `sudo`, so
staying read-only outside the database depends on following the skill. DDP has no
separate remote agent account.

**Discovery.** `ddp help --json` describes every command and flag; `ddp registry
--json` lists registered types and refs; `ddp config schema` prints the config
schema. An agent needs no system prompt full of commands.

## 16. TUI

Bubble Tea. Screens: overview (scheduler, health observation, outbox and jobs),
recent failures, jobs, runs, logs, integrations, models, tables and health.
Keyboard driven. Job actions go through the same command handlers, typed
confirmations, enforced authority and audit path as the CLI. The TUI holds no logic
that `ddp ... --json` cannot produce; it is a view. See [the TUI guide](docs/tui.md).

## 17. Frontend

Workspace: `frontend/packages/ddp-ui` (platform) and `frontend/apps/portal`
(the dealership's portal).

**`@ddp/ui`** provides: the app shell (layout and navigation from `ddp.yaml`, plus
display name, logo and accent color with derived light/dark tokens), auth context,
a route guard that denies by default and reads the same permission names as the
server, the API client (envelope, CSRF, errors as typed values), TanStack Query
hooks, a data table bound to an endpoint (columns, filters, sort, search,
pagination, export), account forms on react-hook-form and Zod, account
administration and the system console. One data-fetching paradigm: TanStack Query
over the shared API client.

**`apps/portal`** ships with login, home, profile, account administration under
explicit permissions, and the operator-only system console (status, jobs, runs,
health and logs). YAML generates navigation and ordinary table pages over
declarative endpoints. Charts, layouts, interactions and workflows are TypeScript
pages under `src/routes`, scaffolded by `ddp new page`; there is no dashboard
language in YAML.

```yaml
pages:
  dashboard:
    { label: Dashboard, path: /, kind: custom, permission: dashboard.read, order: 1 }
  customers:
    { label: Customers, path: /customers, kind: table,
      endpoint: endpoint/customers, permission: customers.read, order: 2 }
  repair_orders:
    { label: Repair orders, path: /repair-orders, kind: table,
      endpoint: endpoint/repair_orders, permission: service.read, order: 3 }
  system:
    { label: System, path: /system, kind: system, policy: admin, order: 100 }
```

A custom page registers its stable YAML id in TypeScript:

```ts
registerPage("dashboard", DashboardPage)
```

The build fails when a custom page has no implementation or TypeScript registers an
unknown page. SQL calculates business metrics, YAML contracts name and define the
returned fields, endpoints authorize and serve them, and TypeScript decides how they
look. Chart components do not calculate business results. Substantial visual
changes belong in TypeScript and CSS rather than an expanding theme registry.

Build: Vite, `tsc --noEmit`, Oxlint, Vitest, Playwright smoke (login, a table page,
the console). The portal loads no third-party scripts, fonts or analytics. The build
output is embedded in the Go binary.

## 18. Deployment and release

V1 officially supports one production topology: one Linux host running Docker
Compose with local Postgres. Managed databases, Kubernetes and multi-host operation
are dealership customizations, not template features.

**Image.** One multi-stage Dockerfile: build the SPA, build the Go binary with the
SPA embedded, then a `python:3.12-slim` final stage with `uv`, the `ddp`, `client`
and `jobs` packages, Postgres client tools, and the binary. One image contains both
runtimes because the scheduler executes Python. It runs as UID 10001 with a
read-only root filesystem.

**Compose** (`deploy/compose.yaml`):

| Service | Image | Notes |
|---|---|---|
| `postgres` | pinned Postgres 17 digest | private data path and healthcheck; never publicly exposed |
| `api` | exact image digest | `ddp api`; `/readyz` healthcheck; host loopback port 8080 |
| `scheduler` | exact image digest | `ddp scheduler`; depends on `api` healthy |
| `cloudflared` | pinned digest | the dealership's tunnel; token from `.env`; automatic updates off |
| `maintenance` | exact image digest | profile-only one-shot commands with owner, readonly or recovery credentials |

The API binds host loopback port 8080. Cloudflared uses the Linux host network to
reach that listener and SSH on localhost; its metrics stay on host loopback.
Running services receive only their component credentials and their own optional
`.env.api` or `.env.scheduler` integration secrets.

The application filesystem is disposable. Postgres is the only durable application
state; job scratch files and generated exports are temporary. A capability requiring
durable files declares an external storage integration. The Postgres data path and a
small backup staging path, both on encrypted storage, are the only persistent local
mounts.

**Backups.** `ddp:backup` creates a custom-format `pg_dump` every 24 hours by
default, validates the archive, encrypts it with an age X25519 recipient and uploads
it over HTTPS to an S3-compatible bucket in the dealership's own account, using
credentials scoped to that bucket. The baseline retains fourteen days; discovery may
tighten frequency or retention. A published manifest records the archive SHA-256,
length, Git revision and exact image digest. The private recovery key stays with the
dealership, outside the registry and the running services.

`ddp-backup-verify.timer` invokes `bin/verify-backup.sh` every Sunday. The script
restores the newest archive with its recorded immutable image into a disposable
database and verifies migrations, table and model contracts and a bounded query for
each declarative endpoint. A backup is not healthy until restore testing is current.
`ddp backup list|run|restore` uses the same implementation; restore verifies the
archive bytes before creating a new database and preserves archived ownership and
grants.

Point-in-time recovery is not part of the baseline. Add base backups and WAL
archiving only when the dealership needs a recovery point shorter than the dump
interval. Recovery combines a verified database backup, the Git revision, the
immutable image digest and secret values held outside DDP. YAML names required
secrets; `ddp doctor` identifies what is missing after restoration.

**Release flow.**

1. A human-reviewed PR merges to the dealership's protected `main`. Required CI runs
   Go, Python, frontend and system checks; then `bin/publish-image.sh` builds
   `ghcr.io/<org>/<repo>:<sha>` for Linux AMD64 and ARM64, checks the embedded
   revision and advances `:stable` by digest. The merge is the production promotion
   event.
2. `ddp-update.timer` runs `bin/update.sh` after boot and five minutes after each
   run. It resolves `:stable` to an immutable digest, compares it with the recorded
   running and failed digests, pulls it and records the pending intent. Compose
   always runs exact digests.
3. The candidate's `ddp deploy plan` reports its migration inventory. When the
   release contains migrations, the updater first creates and verifies a fresh
   off-host backup, stops the scheduler, runs `ddp migrate up` as the owner role,
   then applies SQL-model definitions as the scheduler role.
4. The updater starts the new services and waits for `/readyz`. Startup failure
   restores the previous image digest and its model definitions, records the failed
   digest so the timer does not loop on it, and records a deployment failure for the
   `ddp:deployment` health check, which alerts `platform_ops`. It never retags
   `:stable`.
5. After readiness succeeds, later application or business-health failures alert
   `platform_ops` but do not trigger automatic rollback.

Only outbound access is required from the host. There is no CI runner on the host,
no SSH from CI and no webhook to miss. The updater is one shell script with a lock
file, a state file (`.ddp-release.json`) and the systemd journal. It only joins
systemd, registry and Docker operations; release decisions, migrations and
validation remain in the Go CLI. It changes only the image setting in the protected
host `.env`; host procedures and infrastructure pins use an explicit maintenance
change. There is no self-updating host daemon. See
[deployment](docs/deployment.md).

Configuration follows the same path as code: PR, required CI, image build, deploy and
process restart. Migrations are forward-only and must remain compatible with the
previous image. An automatic rollback changes the image; it cannot undo a database
migration. Destructive contract changes require a later release after the old code
is no longer running. Applied migrations are immutable; checksum drift blocks
startup. Postgres patch releases use the pinned major. A major-version upgrade is an
explicit verified dump-and-restore maintenance operation, never an updater side
effect.

**Remote access.** Each deployment has one Cloudflare Tunnel in the dealership's
own Cloudflare account. Separate routes expose the portal and `localhost:22`; port 22
is never opened to the Internet. The dealership's own Cloudflare Access policy with
MFA protects the SSH route. sshd listens on loopback only, accepts public keys only,
refuses root and password login and admits only the existing non-root administrator
account named at installation. DDP authentication remains authoritative for the
portal. Local console or on-site access is the break-glass path if Cloudflare is
down. Tailscale and Twingate helpers are planned, not built.

Host disks or volumes, off-host storage and backup transport are encrypted. External
database connections require TLS. DDP does not add application-level field
encryption until a dealership's data and threat model require it.

**Install.** `bin/install.sh` runs at the console of a fresh Ubuntu 24.04 host,
from a root-owned checkout at `/opt/ddp` whose HEAD matches the release image. It
refuses existing deployment state. It prompts, without echo for secrets, for the
exact image digest, a read-only GHCR token, the first operator's email and password,
the Cloudflare Tunnel token, backup storage credentials and recovery key, the
encrypted data paths, smoke evidence and the existing administrator account. It
generates every database password, writes a mode-`0600` `.env`, initializes and
provisions the database, bootstraps the first operator, starts the services,
verifies models, records the first deployment, makes and restore-verifies a first
backup and requires a healthy `ddp doctor`. It then restricts sshd as above,
installs the updater and backup-verification units, and enables the timers only
after cloudflared reports an edge connection. Remote portal and SSH acceptance
through Cloudflare remains a separate human check.

## 19. Agent tooling

- **`AGENTS.md`** is the one authoritative guide for Claude, Codex and humans: a
  concise project map, invariants, commands and verification sequence. It points to
  this document rather than copying it. **`CLAUDE.md`** says only:
  `Read @AGENTS.md. It is authoritative.` There is no second agents file.
- **`skills/<name>/SKILL.md`** is the canonical home for action procedures.
  `.agents/skills/` and `.claude/skills/` link to the same directory for discovery;
  they do not contain copies. Human operational guidance lives in
  `docs/runbook.md`.
- Skills are `ddp-onboard`, `ddp-new-job`, `ddp-new-model`, `ddp-new-integration`,
  `ddp-new-route`, `ddp-new-page`, `ddp-investigate`, `ddp-pr-prep`, `ddp-release`,
  `ddp-upgrade` and `ddp-offboard`. Skills describe actions and where to inspect
  facts. They never repeat a dealership's integrations, columns, schedules,
  recipients or business definitions from `ddp.yaml` and code.
- **`ddp validate`** is the compliance gate: required registry sections and fields,
  typed references, entrypoints, DAG cycles, data contracts, layer direction and
  route policies. `make check` adds migration checksums, import boundaries, schema
  freshness and tracked-secret scanning. CI requires them before merge.
- **`ddp new`** scaffolds update the registry and emit the matching code file. YAML
  remains a primary development artifact, not documentation added after the code.
- During work, skills call `ddp check --changed`; before a PR they call the full
  `ddp check`. There is no tool-specific post-edit hook and no MCP server in v1.
  Required CI is the unskippable enforcement boundary.

## 20. Proving ground

DDP maintains a separate proving-ground project with seeded synthetic data under
`tests/proving-ground/`. It is not copied into dealership repos and does not ship in
production images. CI creates it through the same initialization path used for a
real dealership project.

The proving ground exercises one complete path: integration → Python job → landed
table → SQL model → protected endpoint → portal page. It also introduces one
deliberate failure and verifies that `ddp diagnose` reports the failed execution,
error, affected output and upstream context. This is DDP's end-to-end contract. It
complements the fast registry check; it does not run on every production job
execution.

## 21. Onboarding: discovery document to running system

The discovery document is completed by the dealership, usually with its coding agent
interviewing the owner, before onboarding. It has a narrative part for humans and a
structured part that `ddp init --discovery` consumes and copies to
`docs/discovery.yaml`:

```yaml
client:   { name, industry, timezone, locations: [], contacts: [] }
users:    [ { name, email, role } ]
systems:  [ { name, kind: dms|erp|accounting|crm|scheduling|phone|spreadsheet|other,
              vendor, access: api|database|export|scrape|none, docs_url,
              credential_owner: operator|dealership,
              entities: [ { name, deletion_behavior } ] } ]
questions: [ "Which service advisors close the most repair orders per week?", ... ]
outputs:  { pages: [], emails: [ { name, cadence, recipients } ] }
communications: { groups: [ { name: platform_ops, recipients: [] }, ... ] }
hosting:  { mode: self|cloud, domain, exposure: cloudflare, repository }
ownership: { repository, host, domain, cloudflare, backups,
             integration_credentials }        # each operator|dealership
recovery: { backup_interval: 24h, backup_retention: 14d, max_data_loss: 24h,
            target_restore_time: 4h }
constraints: { pii: [], data_retention: "", development_data: synthetic }
success_30_days: [ ... ]
```

The `client` key names the dealership itself (`client.name: Acme Dealership`); the
key name is part of the discovery format the code reads. A DMS is declared as a
system such as `{ name: dms, kind: dms, access: api }`. Ownership values are
`operator` (the operator defined in §1) or `dealership`; a dealership that owns and
runs everything itself uses `dealership` throughout. `hosting.domain` becomes the
portal's `serving.public_url`; `hosting.repository` names the dealership's private
GitHub repository and therefore its image.

`ddp init` generates `ddp.yaml`, a landing-schema migration and `client/<system>.py`
module per system, pending users and roles, navigation, platform health defaults,
alert routing, `.env.example` and an onboarding plan under `docs/`. The `ddp-onboard` skill
then drives an agent: verify each system's documentation and access, write and test
ingest jobs, land data, propose `staging` and `core` models, write `mart` models that
answer the `questions`, declare endpoints and pages, set up the requested emails,
add freshness rules and run `ddp smoke`. The owner logs in and sees their business.

An ingest job is not created until the external system's authentication, usable
access, pagination, rate limits, update cursor and deletion behavior are
established. DDP does not generate disabled jobs as reminders; a declared job is
meant to run once its code and tests pass.

The discovery record explicitly assigns ownership of the repository, host, domain,
Cloudflare configuration, backups and integration credentials. Backup frequency and
restore expectations follow the dealership's dependence on DDP rather than becoming
an unsupported universal promise. Production-derived development fixtures require
explicit approval and human sanitization.

Offboarding is a documented human-run procedure (`skills/ddp-offboard`) for a
dealership taking back control from an IT provider it hired, or decommissioning the
deployment. Taking back control means making and verifying a fresh backup, rotating
the credentials the provider held (tunnel token, backup keys, GHCR token, database
and integration passwords), tightening the Cloudflare Access policy and removing the
provider's accounts. Decommissioning stops schedules and outbound messages, makes
and verifies a final backup, revokes access, removes Cloudflare routes and deletes
copies after the chosen retention period. DDP does not build an offboarding
subsystem.

## 22. Deferred capabilities

OIDC, Redis, non-SMTP communications, a scraping harness, file uploads, live browser
updates, an MCP adapter, bundled Prometheus/Grafana, SQLMesh, CDC, an in-app
assistant, managed Postgres and multi-host operation are not in v1. DDP carries no
interface, service or configuration solely to prepare for them. When a dealership
needs one, its actual requirements determine the smallest seam; a second real
implementation may then justify an interface.

Planned, not built (see `DECISIONS.md` D26): Tailscale and Twingate access helpers,
`ddp secrets set|list`, a restricted `ddp_agent` database role, and updates that wait
for owner approval.

## 23. Understandability and non-goals

DDP has no source-line or cyclomatic-complexity budgets. Numeric ceilings reward
gaming and say little about whether a system is understandable. A fresh agent must
be able to use the registry and standard tooling to locate a capability, its owner,
its entrypoint, its dependencies and its tests without reading the whole repository.

Review enforces focused packages, explicit boundaries, one-purpose jobs, executable
tests and the two-real-implementations rule for new abstractions. `ddp validate`
enforces structural contracts; it does not count lines. The baseline runtime remains
Postgres and Docker, plus cloudflared for remote access.

Non-goals: a visual pipeline designer, abstracting SQL away, multiple schedulers,
multi-tenancy, supporting every architecture, competing with warehouses or
orchestrators on breadth. DDP is one excellent path.
