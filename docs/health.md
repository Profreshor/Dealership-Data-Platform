# Health checks and alerts

`ddp:health` runs every minute through the durable scheduler. It records the
dealership's declared freshness and SQL rules, final job failures and the platform checks below. Inspecting health reads persisted
facts; evaluating it creates a job execution and can enqueue email.

```sh
ddp health --json
ddp health show health/customers_fresh --json
ddp health alerts --json
ddp health evaluate --json
ddp jobs show ddp:health --json
ddp jobs pause ddp:health
ddp jobs resume ddp:health
```

Pausing stops future evaluations. Cancel an active execution with `ddp runs cancel
execution/<id>`. Historical backfill is refused: evaluating current data cannot
reconstruct historical health. `ddp dev` leaves the scheduler off.

## Declare business rules

```yaml
health:
  customers_fresh:
    kind: freshness
    target: table/synthetic.customers
    column: _loaded_at
    max_age: 2h
    severity: warning
    notify: [group/owners]
  invoices_balanced:
    kind: sql
    sql: health/invoices_balanced.sql
    severity: critical
    notify: [group/owners]
```

Freshness uses the maximum value of a declared timestamp column on a table or
model. Missing data is `failing`; a future timestamp or query failure is `unknown`.
SQL rules return exactly one row, with a non-null boolean `ok` and optional text
`message` and `value` columns, in any order:

```sql
SELECT count(*) = 0 AS ok, 'Invoices with missing customers'::text AS message,
       count(*)::text AS value
FROM core.invoices
WHERE customer_id IS NULL
```

Queries run in read-only Postgres transactions with a five-second statement timeout.
SQL files must be regular files inside the project, at most 256 KiB. Results are
limited to two rows for shape validation; message and value limits are 2048 and 512
bytes, enforced before transport. Invalid SQL, shape, size or timeout produces a
bounded `unknown` observation. Query errors do not expose database error details.

## Notification behavior

Every evaluation is retained in `ops.health_evaluations`. `ops.alert_state` tracks
the current incident; `ops.alerts` stores notification decisions separately from
SMTP delivery attempts.

- Entering `failing` or `unknown` creates one alert. Repeated observations do not.
- An unresolved critical rule gets a reminder after 24 hours without a new alert.
- Returning to `ok` creates one recovery notice.
- Final job failures and all `unknown` observations route to `group/platform_ops`,
  the people the dealership names to receive platform alerts.
- Business failures route only to their declared groups. Recovery reaches the
  groups notified during that incident, with a generic recovery message.

Configure SMTP and the referenced groups under `comms`; see [email setup](comms.md).
Missing configuration retains an alert with `notification_error` for the next
poll. Each alert uses a stable outbox effect key. SMTP retries reuse its saved
message, independently of health evaluation. A job still retrying does not create
a new failure incident; a later completed success clears an existing one.

## Platform observations

Platform observations always route to `group/platform_ops`:

| Check reference | Evidence and failure condition |
|---|---|
| `ddp:database` | The configured database accepts a connection within three seconds. Connection errors are `unknown`. |
| `ddp:scheduler` | The scheduler heartbeat says running, is at most five seconds old and is not in the future, and the planner advisory lock is held in this database. Missing, stopped or stale state is `failing`. |
| `ddp:disk` | Available bytes on the filesystem containing the project directory. Below 10% or 1 GiB is a warning; below 5% or 256 MiB is critical. A full filesystem is critical. Invalid capacity or a failed filesystem query is `unknown`. |
| `ddp:outbox` | More than 1,000 pending/delivering messages, an undelivered message older than 15 minutes, or any failed message with retained content produces a warning. Future creation timestamps or query errors are `unknown`. |

Inspect a saved check with `ddp health show ddp:scheduler --json`. Scheduler
status and health share the same heartbeat and lock query. These checks read
metadata; they do not inspect email bodies or contact recipients. Database queries
have three-second deadlines. Thresholds are fixed v1 defaults.

Disk observations describe the filesystem visible to the evaluating process. A
container's project filesystem can differ from the Postgres data volume; this check
does not establish capacity on an unseen volume. Host-level checks are described in
[doctor.md](doctor.md). [Backup commands](backups.md) provide archive and restore
verification; [deployment](deployment.md) covers the production host.

A stopped scheduler cannot evaluate its own failure. An operator can run
`ddp health evaluate`; `ddp status` also exposes the stopped heartbeat and stale
health records. If Postgres is unavailable, an evaluation cannot persist a database
failure or enqueue its alert. Do not treat missing observations as healthy.

See [status and diagnosis](diagnosis.md) for recorded operational reports and
[operational retention](retention.md) for evidence expiry. [Doctor](doctor.md) reads
live platform and host checks without recording alerts. [Metrics](metrics.md) expose
bounded observations on private listeners.

## Backup evidence

When `deploy.backup` is configured, `ddp:backup` uses the persisted backup and
restore records. It requires a verified archive within the declared interval plus
one hour of scheduling allowance, and a successful restore within seven days. A
failed latest attempt or an attempt still running after its recorded timeout fails the check.
It uses the existing alert and recovery path to `group/platform_ops`; it never performs
a backup or restore from the health loop. See [backups](backups.md).
