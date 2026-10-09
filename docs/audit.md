# Privileged operation audit

Privileged changes record their principal, action, target, time and outcome in
`ddp.audit`. PostgreSQL supplies `session_user` and the timestamp. CLI flags and
environment variables cannot override the principal.

## Covered operations

| Action | Target and outcome |
|---|---|
| `migrate.up` | `migration/<kind>/<id>` with `applied` or `failed` and the SQL checksum. |
| `users.bootstrap` | `user/<id>` with `created`; no email, password or hash. |
| `users.invite` | `user/<id>` with invitation status, role IDs and the HTTP actor user ID (empty for CLI). |
| `users.password_set` | `user/<id>` with `changed` and link kind (`invite` or `reset`); no password or token. |
| `models.apply`, `models.refresh` | `model/<name>` with `succeeded` or `failed` and the model-refresh record ID. |
| Manual job operations | See [executions.md](executions.md) for run, backfill, pause, resume and cancellation records. |
| Communication changes | See [comms.md](comms.md) for enqueue, retry, test-send and resend records. |
| `sql.write` | `sql/<SHA-256 of exact SQL>` with `succeeded`, `failed` or uncertain-commit `unknown`; successful command tag and affected-row count. |
| Operational cleanup | See [retention.md](retention.md) for committed batch counts. |

Each successful audit commits in the same transaction as the change it describes.
An audit insertion failure prevents that change from committing. Model failure
history and its failure audit also share a transaction. Read-only inspection,
model planning and verification do not add audit records.

The API has column-level insertion rights for the audit ID, action, target and
outcome. It cannot supply a forged principal or timestamp, change a saved audit or
delete it. Operational retention preserves audit records.

## Migration boundaries

An empty database has no audit table. The initial platform migrations therefore
share a transaction until they create that table and record their successful
applications. A failure before that commit rolls the bootstrap changes and ledger
entries back. The migration runner's empty schema and ledger setup can remain for
the next attempt; no audit store yet exists for a failure record.

Once the audit table exists, each migration commits separately with its ledger and
audit. A later migration failure preserves earlier committed migrations. The runner
records the failed migration after rollback, with a bounded five-second attempt
even if the caller cancelled. If the connection or audit store is unavailable, the
command reports that recording failure as well. It never reports an audit as saved
when recording failed.

Repeated `migrate up` calls do not audit already applied migrations. Upgrading an
older database audits only the migrations this invocation applies; it does not
invent principals for historical ledger entries. Checksums and migration history
remain immutable.

## Scope

Audits describe outcomes, not supplied credentials, SQL text, row values or message
bodies. Failed validation before an operation begins may return an error without
an audit. A transaction that cannot write its audit cannot persist its own failure
record either; the caller receives that error.

Write SQL records success in its data transaction and attempts a failure audit
after rollback. A lost commit acknowledgement is an unknown outcome. See
[SQL command behavior](commands.md#sql-inspection-and-writes) for limits and recovery.
`ddp deploy record` writes its `deploy.record` audit in the same transaction as
the release outcome.
