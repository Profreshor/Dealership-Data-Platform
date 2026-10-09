# Command discovery

Use discovery before choosing an operation:

```sh
ddp capabilities --json
ddp help --json
ddp help jobs run --json
ddp comms resend --help --json
```

The first two commands return the same complete command tree. Targeted help returns
the selected command and its children. Discovery needs no registry, database,
credentials or running services. It lists implemented commands only.

## Development checks

Use the source checkout's installed toolchains and disposable development Postgres:

```sh
ddp check --changed --json
ddp check --json
```

The full command runs `make check`, `make check-smoke` and `make check-image` in
order. It requires the same dependencies as CI, including Docker and Playwright
Chromium. Check output streams to stderr; stdout contains one versioned JSON result.
A failed target stops execution and returns exit 1 with an error. Cancellation
stops the command's process group.

`--changed` compares staged and unstaged files with Git `HEAD` and includes
nonignored untracked files. It runs the affected language and system targets.
Deployment, proving-ground, shared build configuration and unknown file changes
select the full gate. Documentation-only changes run the secret scan; tracked
changes also receive Git's whitespace check. A clean checkout runs no targets.
This mode provides feedback during edits; run the full command before a PR.

`--config` locates the checkout through its `ddp.yaml`. Other registry filenames
are refused because the Make targets use that canonical file. Changed mode needs
a Git repository and an existing `HEAD`; full checks also work in a source copy
without Git. Prepare dependencies and local Postgres with `make setup` and
`make db`. Smoke and image targets manage their own disposable fixtures.

## Migration status and image rollback

`ddp migrate status --json` reports the migrations embedded in the current binary.
It verifies their recorded checksums and marks unapplied entries as pending.
`ddp migrate up` uses the same checks before applying pending migrations.

Applied IDs newer than the binary's latest timestamp in their respective platform
or client ledger are left intact and omitted from this inventory. The client
ledger, `ddp.client_migrations`, records the dealership's own migrations under
`migrations/app`. This allows an older image to start after a forward release. Unknown IDs at or before that
timestamp, invalid IDs and changed embedded checksums fail. Migration history alone
cannot prove SQL compatibility or distinguish a deleted tail from a newer release;
release review and rollback tests must enforce those requirements.

## Release preflight and outcomes

Run `ddp deploy plan` from the candidate image with the owner maintenance database
credential:

```sh
ddp deploy plan --current "$CURRENT_IMAGE_DIGEST" --image "$CANDIDATE_IMAGE_DIGEST" --json
ddp deploy plan --current "$CURRENT_IMAGE_DIGEST" --image "$CANDIDATE_IMAGE_DIGEST" --failed "$FAILED_IMAGE_DIGEST" --json
ddp deploy status --json
```

References must be immutable GHCR digests from the deployment's configured repository.
The candidate must embed a full Git revision. The plan returns `unchanged`,
`rejected` for the recorded failed digest, or `apply`. For `apply`, it checks the
embedded migration inventory against Postgres and returns pending entries plus
`requires_backup`. Pending migrations without backup configuration are refused.
The plan uses a read-only transaction; it does not verify storage credentials,
create an archive, apply migrations/models, resolve registry tags or start services.

After the host establishes a release outcome, owner maintenance records its bounded
facts and an audit entry in one transaction:

```sh
ddp deploy record --current "$PREVIOUS_IMAGE_DIGEST" --image "$CANDIDATE_IMAGE_DIGEST" \
  --revision "$CANDIDATE_REVISION" --status succeeded --phase ready --json
```

Use `failed` with phase `preflight`, `backup`, `migrations`, `models`, `startup` or
`rollback` for failures. Success requires phase `ready`; recording it asserts that
the host has already verified readiness. `deploy status` returns the latest outcome
or `deployment: null` when none exists. API, scheduler and readonly roles can inspect
these records but cannot write them. Historical records remain intact.

After the first recorded outcome, `ddp:deployment` joins health, doctor and the
existing alert/outbox path. Repeated evaluation of one failure emits one alert;
a later successful outcome emits one recovery. An unavailable ledger reports
unknown. A proven empty ledger leaves this check inactive.

The pull-based [host updater](deployment.md#enable-automatic-releases) uses these
commands for its database preflight and operational evidence.

## Runtime commands

Use [`ddp provision <component>`](deployment.md#initialize-a-fresh-database) after
initial migrations to create or rotate the production database logins. It needs
administrator `DATABASE_URL` and `<COMPONENT>_DATABASE_PASSWORD`; `--json` returns
only the login and group names. Passwords never appear in command arguments.

For production Compose, run `ddp api` and `ddp scheduler` as separate processes.
Each uses its own `DATABASE_URL`; Python jobs receive `JOB_DATABASE_URL` with the
job role's grants. Migrations run separately before either service starts.

Configured backups run as `ddp:backup` in the scheduler. Supply its separate
`BACKUP_DATABASE_URL`, storage credentials and exact `DDP_IMAGE_DIGEST`; see
[backup credentials and recovery](backups.md). Restore credentials and the private
age key belong to host maintenance, outside running services.

Use `ddp serve --all --json` to run both loops in one process. Set `DATABASE_URL`
for the API and `SCHEDULER_DATABASE_URL` for the scheduler, pointing to the same
database with their respective credentials. Both are required. Each loop uses
its existing implementation and private metrics listener. Set `--api-metrics-addr`
and `--scheduler-metrics-addr` to change their default addresses,
`127.0.0.1:9091` and `127.0.0.1:9092`. A failure stops both loops; shutdown waits
for both to release their resources. Normal shutdown emits one `stopped` result.

Development starts local Postgres, applies migrations and runs the API with Vite:

```sh
ddp dev
ddp dev --scheduler
ddp dev --scheduler --comms
```

Scheduling and SMTP delivery are off by default. `--scheduler` enables the declared
jobs, model schedules, health and cleanup. `--comms` also enables the configured
SMTP relay and requires `--scheduler`. Review the external effects of the
dealership's own jobs and its integration credentials before enabling scheduled
work. These flags control platform scheduling and SMTP; the project's Python code
can still perform its declared external effects.

Development selects the local Compose database, or the explicit PostgreSQL URL in
`DDP_DEV_DATABASE_URL` for the development container. It derives API, scheduler and
job role connections from that one database. Ambient `DATABASE_URL`,
`SCHEDULER_DATABASE_URL` and `JOB_DATABASE_URL` do not select development databases.
The selected development credential must apply migrations and assume the component
roles. Vite or service failure stops the development runtime, including child
processes. Development accepts the same two metrics-address flags as `serve`.

## Configuration diffs

Compare the current registry with the same file in Git `HEAD`, or supply a baseline
file explicitly:

```sh
ddp config diff --json
ddp config diff /tmp/previous-ddp.yaml --json
```

Both inputs must pass configuration validation. Comparison uses parsed values, so
comments, formatting and object key order do not produce differences. It does not
check historical entrypoints on disk or resolve environment secrets. The explicit
file form needs no Git repository or database.

The result lists sorted JSON Pointer paths with `add`, `remove` or `replace`, plus
the before and after values. Arrays are compared as whole values. An empty change
list returns exit 0; differences return exit 3 with the report; invalid inputs
return exit 1. This compares local declarations, not deployed runtime state.

## SQL inspection and writes

Use the grants of the credential in `DATABASE_URL`:

```sh
ddp sql 'SELECT current_date' --json
ddp sql 'SELECT * FROM mart.customers LIMIT 50' --limit 50 --timeout 10s --json
```

The default transaction is read-only. The command accepts one statement and refuses
transaction controls, session controls and `COPY`. PostgreSQL parses the statement
through its extended protocol, even if the connection selects simple protocol.
No registry or application secrets are required.

Results contain `columns` (name and PostgreSQL type OID), positional `rows`,
`command` and `rows_affected`. Cells contain PostgreSQL text values; SQL NULL becomes
JSON `null`. Positional columns preserve duplicate names and text preserves numeric
precision. Empty arrays remain `[]`.

The default limits are 1,000 rows and 30 seconds. Set `--limit` from 1 to 10,000
and `--timeout` above zero through five minutes. SQL is limited to 256 KiB and
returned cell text to 4 MiB. Exceeding a result limit returns an error without
partial output and rolls back transactional writes. The driver receives a whole
wire row before enforcing the byte limit; this is not a database memory limit.
Cleanup and failure-audit attempts have separate five-second bounds.

For a write, review the exact statement and supply `--write --confirm <sha256>`.
A missing or incorrect confirmation returns exit 4 and the required fingerprint
before connecting. Compute it from the exact SQL bytes, without an added newline.
Database grants must permit both the mutation and its audit insert. Success commits
the mutation and `sql.write` audit together. Errors expose safe categories or
SQLSTATE codes; they omit SQL text and row values.

A failed attempt rolls back and tries to record a failure audit. The command reports
an unavailable audit store or connection; it does not claim the audit was saved.
A failed commit acknowledgement reports an unknown outcome. Read the audit and
actual database state before retrying. SQL functions can have external effects
that a database rollback cannot undo; use credentials with the intended grants.

## Portal accounts

The `users` commands manage portal accounts with the account-administration grants
of the credential in `DATABASE_URL`; [accounts](accounts.md) describes each one:

```sh
ddp users list --json
ddp users invite --email person@example.test --role reader --json
ddp users update USER_ID --role reader --disabled=false --json
ddp users disable person@example.test --json
ddp users disable provider@example.test --confirm provider@example.test --json
ddp users roles save reader --name Reader --permission customers.read --json
ddp users bootstrap --email owner@example.test --json
```

`users disable` accepts an email or user ID and works on any account. An operator
account requires `--confirm` with its exact email; a missing or different
confirmation returns exit 4 and changes nothing. The account loses its roles,
operator flag, sessions and password links in one transaction with its
`users.disable` audit, after which `users bootstrap` can create a replacement
operator once no operator account remains.

## JSON contract

Discovery commands return a recursive command record in the standard version 1
envelope:

- `name`, `usage`, `description`, `details` and `aliases` identify the command.
- `runnable` distinguishes direct handlers from command groups.
- `risk` is `read`, `write`, `external` or `service`. A command with a preview uses
  the risk of its applying mode; its confirmation rule explains the preview.
- `required_role` describes the required operating authority below.
- `confirmation` states the concrete flags and conditions checked by the handler.
- `audit` distinguishes transactional audit, operational history, no audit and
  required audit work that remains missing.
- `flags` lists each effective flag once, with type, shorthand, description,
  declared default and Cobra's required-flag marker. Handler validation can impose
  further conditions, stated in the command description or confirmation rule.
- `commands` contains child records. Empty lists are `[]`.

Flags are sorted by name. Discovery emits declared defaults, never the supplied
flag values or environment values. Passwords remain environment inputs and have no
CLI flag. Cobra's generated shell-completion commands are disabled because they
emit shell text outside this JSON contract.

## Operating authority

| Role | Required access |
|---|---|
| `local_reader` | Read the relevant local files; doctor also probes available host, database and SMTP services. |
| `database_reader` | A database credential with access to the inspected relations. |
| `developer` | Write the source checkout or run its disposable local environment and checks. |
| `operator` | Database grants for the requested job, model or communication operation. |
| `administrator` | Schema/migration or account-administration privileges; initial provisioning also creates component roles. |
| `service` | The appropriate API or scheduler database grants, listener access and declared runtime secrets. |

These classifications describe requirements. Discovery does not check the caller's
credentials or grant access. PostgreSQL grants and the OS establish authority;
concrete handlers retain their confirmations and transaction boundaries. There is
no `--as`, environment principal override or generic action dispatcher.

## Contributor contract

Register each Cobra command through the local command catalog with its policy beside
its handler. The catalog supplies help and capabilities from the actual command tree.
Startup rejects incomplete declarations; execution also rejects an undeclared
command. Flags stay on their Cobra command, with no second flag registry.

Migration, bootstrap and model writes record [privileged audits](audit.md).
