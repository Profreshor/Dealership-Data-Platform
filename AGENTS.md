# Repository Guidelines

This file is the authoritative guide for coding agents and humans. It is copied
verbatim into every dealership repository created with `ddp init`.

## Project Structure & Module Organization

DDP (Dealership Data Platform) is a free, MIT-licensed foundation for a dealership's
data system: one Go binary (`ddp`), one Python job kit, one TypeScript portal shell,
one Postgres database and one Docker image. Read `README.md`, then `VISION.md`
(purpose), `DECISIONS.md` (settled choices) and `ARCHITECTURE.md` (how it fits
together). Decisions take precedence over architecture; the code is the final
authority on current behavior. Feature guides live in `docs/`.

Platform paths (keep changes deliberate so template upgrades merge cleanly):
`cmd/ddp/`, `internal/ddp/`, `ddp/` (Python kit), `migrations/ddp/`,
`frontend/packages/ddp-ui/`, `deploy/`, `bin/`, `skills/`, `AGENTS.md`, `CLAUDE.md`.

Dealership paths: `ddp.yaml` (the registry), `internal/app/` (Go features, registered
in `internal/app/register.go`), `client/` (the dealership's shared Python, by
integration), `jobs/` (flat, one purpose per job), `models/{staging,core,mart}/`
(one SQL relation per file), `health/` (SQL health rules), `migrations/app/`,
`templates/comms/`, `frontend/apps/portal/`. The `client/` directory and "client
routes" hold the dealership's own code.

Platform code never imports dealership code: `internal/ddp` does not import
`internal/app`, the `ddp` Python package does not import `jobs`, and `@ddp/ui` does
not import `apps/*`.

## Build, Test, and Development Commands

Toolchain: Go 1.27.1, Python 3.12 through uv, Node 22 or newer, Docker Compose.
The `.devcontainer/` provides the same toolchain with a private Postgres service.

- `make setup`: download Go, Python and frontend dependencies.
- `make db`: start the local development Postgres (not needed in the devcontainer).
- `make build`: build the frontend and `build/ddp`.
- `make check`: Go, Python, frontend and system checks; the CI baseline.
- `ddp check --changed --json`: affected checks during edits.
- `ddp check --json`: the full gate before a PR (`make check`, `make check-smoke`,
  `make check-image`; needs Docker and Playwright Chromium).
- `ddp validate` and `ddp config validate`: registry and entrypoint validation.
- `ddp smoke --json`: the ingest-to-portal acceptance gate.
- `ddp dev`: local API and Vite portal; add `--scheduler` and `--comms` only when
  you intend to run jobs and send email.
- `ddp --json`, `ddp help <command> --json`, `ddp capabilities --json`: discover
  commands, risk and required authority.

Create jobs, models, integrations, routes, pages, health rules and migrations with
`ddp new`, which updates `ddp.yaml` together with the code. Run `make schema` after
changing configuration structs.

## Coding Style & Naming Conventions

Go is `gofmt`-clean and passes `go vet`. Python uses Ruff (line length 100), strict
Pyright and import-linter. TypeScript uses `tsc --noEmit`, Oxlint and Vitest. YAML
uses two-space indentation. Use typed references such as `integration/dms`,
`job/sync_dms_customers` and `table/dms.customers`. Package code by business
capability, never by technical layer (`extract/`, `handlers/`, `utils/`).
Timestamps are `timestamptz` in UTC; money is decimal, never floating point.

When code changes a job's purpose, inputs, outputs, schedule or contract, update
`ddp.yaml` in the same change. Never put secret values in YAML, code, fixtures or
logs; secrets belong in protected environment files on the host.

## Testing Guidelines

Go tests, pytest, Vitest and Playwright. SQL is tested against real Postgres, never
mocks or SQLite; set `TEST_DATABASE_URL` to a disposable database. Migrations are
tested as a chain from an empty database, and applied migrations are immutable. Use
synthetic or small, manually sanitized fixtures; never production data. Add a focused
test for each new failure path. There is no coverage percentage gate.

## Commit & Pull Request Guidelines

Use concise conventional subjects such as `feat:`, `fix:`, `test:` and `docs:`.
Keep each PR focused on one concern. Describe the outcome and verification, update
affected docs and registry declarations, and run the full `ddp check` before review.
A human approves every merge to `main`; a merge to `main` publishes a release that
the dealership's server installs automatically.

## Production Boundaries

Development happens on a development computer or in the devcontainer, never on the
production host. Do not create accounts, keys, credentials or access paths. Do not
ask people to paste secrets into a chat; humans type secrets into the program that
prompts for them or into protected environment files. For production investigation
use `skills/ddp-investigate` (read-only commands with the `ddp_readonly` login) and
only access the dealership has already granted. Use the procedures under `skills/`
for onboarding, releases, upgrades and offboarding.
