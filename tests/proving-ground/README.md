# Synthetic proving ground

This fixture exercises HTTP ingestion, transactional landing, a three-layer SQL
model chain, local authentication and an embedded table page. Its source service
and test credentials are for disposable development only.

On a Docker-capable host, after `make setup`:

```sh
npm exec --prefix frontend -- playwright install chromium
make check-image
```

The check builds the empty template image and a separate initialized fixture image.
It uses production `deploy/compose.yaml` with synthetic overrides, starts a fresh
database, applies migrations, and provisions separate component logins through the
CLI. Ingest uses the job login; model apply uses the scheduler login. The
scheduler runs the native refresh job and the API serves the protected portal.
Permission groups stay `NOLOGIN`. Runtime checks inspect hardening, secret
isolation, writable staging and private database networking. PostgreSQL statement,
duration and transaction logs are checked for provisioning credential leaks.
Bootstrap activates the pending operator created by initialization and preserves
their account ID. Playwright logs in, verifies both customer rows and logs out. The
operations check pauses ingestion and ages only synthetic `_loaded_at` timestamps
to cross the declared five-minute freshness limit without waiting five minutes.
Repeated evaluations must produce one alert; resumed ingestion must produce one
recovery notice. Both messages must be rendered and delivered by the SMTP outbox.
The separate SMTP fixture uses TLS with a temporary test certificate. The retry
check waits for a scheduled relay execution started after its test message exists.
It then stops the synthetic source and checks the resulting failed execution through
CLI diagnosis, a real TUI terminal and the browser System console. All three must
identify the same execution and its final attempt. Recovery preserves that failed
execution's history.

The check removes its containers, volume, image tags and temporary checkout on exit.
Port 18081 must be available. CI also installs Chromium's operating-system
dependencies with `playwright install --with-deps chromium`.

The source-checkout smoke gate also works without Docker access when a disposable
Postgres service is already available:

```sh
make check-smoke
```

`TEST_DATABASE_URL` must name a development Postgres login that can create databases
and roles. Put its host, credentials and database in the URL authority and path;
connection overrides in query parameters are rejected. Normal parameters such as
`sslmode` are supported. `make check-smoke` creates a separate fixture checkout and
an HTTP source on a free local port, then invokes the actual `ddp smoke --json`.
The CLI creates its own empty database and temporary job login, migrates, ingests,
checks model contracts, bootstraps an administrator and verifies every rendered
table value in Chromium. It removes the database and login on success or failure.
For an already configured dealership repository, invoke `ddp smoke` directly with its
declared integration secrets. This deliberately runs its selected integration.

The template image runs as UID 10001 and contains the Go binary, embedded portal,
Python job kit, the dealership's own code under `client/`, SQL models, uv and
Postgres 17 client tools. The build context excludes secrets and generated
dependencies. The final image excludes the fixture, test source and build tools.

`prepare.py` builds the candidate initializer from a temporary committed snapshot
of tracked source files, then runs dry-run and `ddp init` with `discovery.yaml`.
Both the smoke and image gates use this path. Discovery-owned identity, integration
facts, groups and deployment settings survive assembly. The original discovery
migration remains unchanged.

After initialization, assembly adds the tested `client/` modules, jobs, models and
registry sections from `implementation.yaml`. `ddp new migration` adds
`customers.sql` after the generated seed, using its existing landing schema.
Assembly adds the disposable SMTP service settings and substitutes local source
and portal addresses. The full registry exists in
the initialized checkout; `implementation.yaml` is a fixture overlay, not a second
standalone registry. `make check-init` separately verifies a freshly initialized dealership repository before
this implementation is added, including its expected failure at the smoke gate.

Smoke selects one ingest-to-page path through declared
model dependencies. The fixture uses a staging view, a core materialized view and
a mart view. Ambiguous or disconnected paths fail before database creation.
The extended image gate also checks publication, rollback, encrypted backup and
restore on disposable Docker infrastructure.
Native installation and real Cloudflare transport remain separate acceptance gates.

For the agent onboarding acceptance exercise, give the agent `AGENTS.md`,
`skills/ddp-onboard/SKILL.md`, `discovery.yaml` and the accompanying
[discovery narrative](onboarding-discovery.md). Supply the local source origin and
synthetic credentials separately. The agent implements a fresh dealership repository from those
facts; copying `implementation.yaml` or fixture source does not prove this gate.
