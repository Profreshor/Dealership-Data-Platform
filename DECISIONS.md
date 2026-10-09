# Dealership Data Platform Decisions

This file records DDP's current architectural decisions as of the public release,
2026-10-09. Git preserves earlier thinking; this file does not preserve superseded
decisions or force readers to reconstruct the present from a chain of reversals.
New decisions take the next number. Reversing a decision replaces its entry and
says what changed.

Reading order: `VISION.md` (why) → this file (what is settled) →
`ARCHITECTURE.md` (how it fits together and how the code is laid out).

---

## D01 — DDP is a free foundation, not a product or a service

DDP is the opinionated platform a dealership's coding agent builds on: scheduler,
database layout, job kit, portal, email, health, deployment and operator tools.
The dealership's own jobs, reports and pages supply the business capability. DDP
does not replace knowing the business, deciding what matters or reviewing changes.

**Rules out:** positioning DDP as SaaS, a hosted service, a universal data solution
or a button that understands a dealership's business automatically.

## D02 — Every dealership runs its own complete, private repository

A dealership repository begins from the DDP template through `ddp init` and contains
the entire platform plus that dealership's code and configuration. The dealership
owns the GitHub repository, the release images in its own package registry, the
server, the data and every service account the system uses.

Template improvements enter a dealership repository through an ordinary Git merge
from a tagged DDP revision, followed by a human-reviewed PR. There is no custom
package manager, upgrade engine, fleet control plane or multi-tenant DDP service.
Taking an improvement is always optional.

## D03 — The spine stays recognizable while dealership capabilities vary

The scheduler, registry, runtime contracts, database conventions, serving layer,
deployment and operator tools form the stable spine. Dealership-specific jobs,
models, integrations, endpoints and pages supply the business capability. Any layer
may be changed when a real dealership requires it, but divergence is deliberate and
reviewed, and changed platform paths may need manual work during a later upgrade.

A dealership capability moves into the template only after it proves useful beyond
one dealership. DDP does not speculate about every future need or edge case.

## D04 — One deployable and one operational model

DDP is one repository, one application image, one Go binary and one Postgres
database with separated schemas and roles. Docker Compose runs `api`, `scheduler`,
`postgres` and `cloudflared` on one Linux host. The image also contains the Python
runtime used for jobs.

**Rules out for v1:** microservices, Kubernetes, Redis, a separate application
database, a separate portal backend, multi-host scheduling and bundled Prometheus or
Grafana.

## D05 — `ddp.yaml` is the canonical project registry

One root `ddp.yaml` tells a human or agent what exists and how it connects. Every
supported top-level section is present, even when unused; `{}` means supported but
unused, while a missing required section is invalid. Includes will be added only if
a real repository makes the single file unworkable.

The registry contains identity, contracts, typed relationships and operating policy.
Code contains joins, calculations and behavior. References are typed, such as
`integration/dms`, `job/sync_dms_customers`, `table/dms.customers`,
`model/mart.customers`, `endpoint/customers` and `page/customers`.

Each relationship is declared once at its natural owner. Jobs and models declare
what they read and write; reverse relationships are derived. A schedule belongs to
its job. The registry is not split into one tiny YAML file per declaration.

## D06 — The registry is enforced, not aspirational documentation

Go structs define the configuration contract and generate a committed JSON Schema.
`ddp validate` checks required fields and sections, typed references, entrypoints,
contracts, layer direction, duplicate names and DAG cycles. It is a required CI
check, so an incomplete registry cannot merge.

The author of a code change must update the registry when purpose, inputs, outputs,
schedule or contracts change. Static checks cannot infer every semantic change in
arbitrary code; agent procedures and human review supply that due diligence.

Configuration follows the normal PR, CI, image and deployment path. Production has
no hot reload or database-backed configuration service. Secrets never appear in the
registry; protected environment files on the host are the baseline secret store.

## D07 — Integration is the broad external-system term

An integration is an external system DDP reads from or writes to: the DMS, CRM,
accounting, OEM portals and anything else. A source is an integration used as
input, not a parallel top-level concept. Understanding the external system is a
prerequisite to creating the integration and its jobs.

Authentication, pagination, cursors, rate limits, late data, deletion semantics,
deduplication and idempotency follow the external system's actual guarantees. DDP
does not default unknown behavior off or hide it behind a universal connector model.

## D08 — Go owns the system; Python owns bounded data work

Go owns configuration, validation, scheduling, migrations, process supervision,
auth, HTTP serving, health, communications, deployment commands and other durable
system behavior. Systems-heavy integrations such as CDC or file watching also
belong in Go.

Python jobs perform one bounded dealership operation through a standard runner.
They are strictly typed with Pyright, receive a narrow run context from Go and never
parse the full registry or implement a second scheduler. The shared Python kit
provides the small database, logging, HTTP, landing and result primitives that jobs
genuinely repeat; it does not provide a universal REST client or speculative
connector framework.

SQL expresses transformations. TypeScript implements the portal. Interfaces are
introduced only when at least two real implementations need the same seam.

## D09 — A job has one purpose and an explicit execution contract

Each job declares its purpose, action, entrypoint, reads, writes, schedule, timeout,
retry behavior and write semantics. Ingestion jobs also declare the applicable
deletion, deduplication and idempotency strategy. Idempotency is required where a job
can be replayed; its exact mechanism follows the external system and destination.

The Go scheduler invokes only the standard Python runner, not arbitrary shell
commands. Jobs begin in a flat `jobs/` directory and may later group by business
capability or integration, never by generic action folders such as `extractors/`.
Shared dealership behavior belongs in specifically named modules under the `client/`
Python package, which holds the dealership's own shared code.

DDP does not impose line-count or cyclomatic-complexity limits. A job is split when
part of it needs independent scheduling, retries, observability, reuse or testing.

## D10 — Scheduling is durable, inspectable and deliberately small

One scheduler process is protected by a Postgres advisory lock. It persists ticks,
executions and attempts, resolves a typed acyclic job graph, applies bounded worker
concurrency and records enough context to explain every run.

Retries, backfills, catch-up, timeouts, dependency failures and constrained external
systems have explicit semantics. A watermark advances only after committed output
and a successful validated result. Job execution does not perform a full registry
or database audit; periodic health checks raise alarms separately.

## D11 — Postgres is both the data platform and durable operational record

Schemas separate DDP metadata, operational state, application data, landed
integration data, `staging`, `core` and `mart`. Component roles receive only the
access they need. Table contracts declare stable names, columns, compatible types,
nullability and constraints, plus who reads and writes them. They do not repeat SQL
joins or calculations.

Migrations are plain SQL executed by Go. Platform (`migrations/ddp`) and dealership
(`migrations/app`) migrations have separate directories and ledgers, use sortable
timestamp IDs, are checksummed and become immutable once applied. Platform
migrations run first. Grants and ownership are declared when an object is created or
changed rather than repaired by a generic grant reconciler.

Postgres stores timestamps as `timestamptz` in UTC. The dealership's timezone
controls scheduling and display. Money is decimal end to end, never floating point.

## D12 — SQL models remain plain and honest

A model file contains exactly one `SELECT` and materializes as a view or materialized
view. Its registry entry declares identity, inputs, output, layer and refresh policy;
a materialized view declares a usable unique key. SQL itself remains the authority
for joins, filters and calculations.

Models are dependency-ordered, checked against real Postgres and refreshed by DDP.
They never become a second migration system. SQLMesh or another transformation
framework may be adopted later only when a dealership demonstrates the need; DDP
does not pre-build a seam for it.

## D13 — The serving layer is declarative until custom code is warranted

The baseline portal serves registered tables through protected endpoints and table
pages. YAML may declare navigation, columns, filters and permissions; it is not a
dashboard or business-logic DSL. Custom Go routes and TypeScript pages register
under stable IDs when the declarative path stops fitting.

The server is authoritative for authorization. V1 policies are Public,
Authenticated, Permission and Admin. Search uses Postgres, and large exports stream
bounded CSV responses. The portal contains login, account administration, table
pages and an operator-only System console; further UI grows from dealership needs.

The frontend uses React, Vite, TypeScript, TanStack Router, Query and Table,
Tailwind, react-hook-form, Zod, Vitest, Playwright and Oxlint. There is one
data-fetching paradigm.

## D14 — Authentication is complete but not abstract

V1 uses invite-only local accounts, Argon2id password hashes, password reset by
email and opaque Postgres-backed sessions whose tokens are stored only as hashes.
Permissions are resolved on every request so revocation takes effect immediately.
Operator accounts administer the platform; other users receive explicit permissions
through roles.

There is no Redis seam, auth-provider interface, session snapshot or OIDC
configuration in v1. Those are added when a dealership actually needs them.

## D15 — SMTP, health and observability use concrete v1 behavior

SMTP is the only v1 communication transport. Messages are rendered once, and their
exact content is persisted before delivery. Automatic retries and deliberate manual
resends remain distinct operations. There are no attachments and no generic
transport interface until another transport is required.

Dealership health rules are either freshness checks or explicit SQL checks. Go owns
platform health. States are `ok`, `failing` and `unknown`. Routing uses explicit
recipient groups: platform failures always go to `platform_ops`, the people the
dealership names to receive platform alerts, and business rules go only to the
groups they name. There are no default recipients. Both processes expose bounded
Prometheus metrics on loopback, but DDP does not ship a monitoring stack.

## D16 — The CLI is the operator and agent interface

The Cobra CLI presents human output by default and versioned JSON with `--json`.
The Bubble Tea TUI reads the same Go packages and covers overview, failures, jobs,
runs, logs, integrations, models, tables and health. It expands only after the CLI
is stable.

`AGENTS.md` is the sole authoritative agent guide. `CLAUDE.md` contains only:
`Read @AGENTS.md. It is authoritative.` Canonical action procedures live under
`skills/`; the `.agents/skills` and `.claude/skills` discovery directories link to
them. Skills teach actions and where to find facts. They do not duplicate registry
content.

V1 has no `SKILLS.md`, MCP server, generic `act` command, alternate CLI principal or
tool-specific enforcement hook. Required CI is the enforcement wall.

## D17 — Agents develop through reviewed branches, never production

Development agents work on a development computer or in the standard development
container without access to the Docker socket, against synthetic data. Branch rules
require human-reviewed PRs. Production credentials and production data do not enter
development by default.

Production investigation is done by people the dealership has given access, at the
host console or over the dealership's own Cloudflare SSH route. They follow the
`ddp-investigate` procedure: read-only `ddp` commands through the maintenance
container with the `ddp_readonly` login, so PostgreSQL itself refuses writes. DDP
creates no remote account, key or access path for anyone. An agent that reads
production data sends what it reads to its AI provider, so giving one production
access is the dealership's deliberate decision.

## D18 — Production releases are pull-based and digest-pinned

CI in the dealership's repository builds an image tagged by commit SHA after a
human-reviewed merge to protected `main` and advances the `:stable` pointer in the
dealership's own registry. A small host-side systemd updater resolves that pointer,
pins the immutable digest, verifies a fresh backup when migrations are present, runs
the one-shot migration command, restarts Compose and waits for readiness.

On startup failure it restores the recorded previous image digest and marks the bad
digest. It never retags `:stable` and never reverses a database migration. Migrations
must remain compatible with the previous image. Configuration changes use the same
release path as code. Only changes merged into the dealership's own repository
reach its server.

## D19 — The dealership's own Cloudflare account provides the remote path

Each deployment has one Cloudflare Tunnel in the dealership's own Cloudflare
account, with separate portal and SSH routes. The portal is protected by DDP's own
authentication. The dealership's own Cloudflare Access policy with MFA protects the
SSH route. sshd listens on loopback only, accepts keys only, refuses root and
password login, and admits only the existing non-root administrator account the
dealership names at installation. Public port 22 is never opened.

Local console or on-site access is the break-glass path if Cloudflare is down.
Tailscale and Twingate helpers are planned (D26), not built.

## D20 — Backups and data custody are part of the baseline

DDP creates a custom-format Postgres dump every 24 hours by default, verifies it,
encrypts it with the dealership's own age key and sends it over HTTPS to an
S3-compatible bucket in the dealership's own account. Fourteen days are retained by
default. A weekly host timer restores the newest backup into a disposable database
and verifies its migrations, contracts and declared endpoint queries. Point-in-time
recovery is added only when the dealership needs a smaller recovery point.

The data, the backups, the private recovery key and every credential stay with the
dealership. The host keeps the private recovery key in its protected `.env` because
the weekly restore test needs it; the dealership also keeps an offline copy away
from the host so it can restore if the host is lost. Tests use synthetic or small
manually sanitized fixtures. Business-data retention and cleanup require explicit
dealership jobs or migrations; DDP automatically cleans only its own operational
data. Deployment and audit metadata is retained for the life of the deployment by
default.

## D21 — Onboarding begins with facts and ends with a hard smoke gate

Discovery records the dealership's systems, access, users, definitions, reporting,
automation, ownership and recovery requirements. Each ownership field is `operator`
or `dealership`. The operator is the person who runs the system for the dealership:
normally someone at the dealership, or an IT provider the dealership hires, with
access the dealership grants and can revoke. `ddp init` turns those facts into
filesystem artifacts only. It does not connect to production, invent placeholders or
pretend an integration works.

The development scheduler and communications are off unless explicitly enabled.
Real integration probes are deliberate. The dealership creates the first operator
account through a bootstrap command. A deployment is not complete until `ddp smoke` passes.

## D22 — CI and the proving ground protect the spine

Required CI runs independent parallel Go, Python, frontend and system/registry
stages. Changed checks provide fast local feedback; the full suite is authoritative
before merge. It verifies the registry, migrations, generated schema, language
checks, security checks and the complete proving-ground path.

The proving ground is a separate deployment with seeded synthetic data. It exercises
registry → validation → migration → Python ingest → landed table → SQL model
→ protected endpoint → portal page, plus one deliberate failure and diagnosis.
It is not copied into dealership repositories or shipped in production images.

## D23 — V1 is frozen around one walking skeleton and one dealership operation

V1 gains scope only when the proving ground or a real dealership deployment requires
it. The thinnest complete path runs through configuration, typed references,
validation, Postgres, migrations, the CLI, one Python ingestion job, one SQL model,
one protected endpoint and one table page.

V1 is shaped by a dealership operation, from a single store to a dealer group, with
sales, service, parts, F&I and accounting, and a DMS reached through its API as the
main source. What DDP must do well is a principal- and GM-facing portal, scheduled
summary email, and status, health and alerts delivered to people the dealership
names. Success is a clean deployment that delivers useful results soon after the
dealership-specific work is done.

## D24 — MIT license, no support, no warranty

DDP is provided as is under the MIT license. Anyone may use, modify and deploy it.
There is no vendor, no support, no warranty, no service level and no commercial
terms. The dealership's coding agent is the first line of help: it reads the repo's
docs and skills, runs `ddp doctor`, `ddp status --json` and the diagnose commands,
and proposes changes the dealership reviews. Issues and pull requests on the public
repository are welcome, with no promise of a response.

**Rules out:** license keys, paid tiers, features gated behind an account, any
runtime dependency on the project or its authors, and documentation that assumes
someone else will run the system.

## D25 — No telemetry

Nothing in DDP reports usage, data, errors or metrics to the project authors or
anyone else. Prometheus metrics, logs, audit rows and backups stay on the
dealership's server or in storage it owns. The deployed system's only outbound
connections, all to accounts the dealership controls or chooses, are:

- the dealership's own GitHub package registry, for release images;
- Docker Hub, for the pinned Postgres and cloudflared images;
- Cloudflare, for the dealership's tunnel;
- the dealership's SMTP provider;
- the dealership's backup storage;
- the external systems the dealership's own jobs call.

The dealership chooses which AI provider, if any, its coding agent uses.

**Rules out:** usage telemetry, crash reporting, update pings or metrics sent to the
project authors or anyone else; a project-operated monitoring service; default alert
recipients outside the dealership.

## D26 — Planned features are labelled, never implied

Some features were designed before the code existed and are not built. They are
documented only under "Planned" headings and never described as existing behavior:

- Tailscale and Twingate access helpers, and an office-network-only mode;
- `ddp secrets set|list` for entering and checking secrets without editing files;
- a restricted `ddp_agent` database role that sees only business data the
  dealership explicitly grants;
- updates that wait for the owner's approval or a maintenance window.

Until they exist, the documented paths are the real ones: Cloudflare Tunnel (D19),
protected environment files typed by a human (D06), the `ddp_readonly` login for
investigation (D17) and automatic release after a reviewed merge (D18).
