# Executions, retries and history

`ddp jobs run job/<name>` saves a queued execution, claims it, and waits for its
attempts to finish. Each retry retains the execution ID and scheduled time, creates
a new attempt ID, and uses the retry policy saved when the execution was queued.
Registry edits affect the retry policy of new executions. Capped exponential delay
with jitter separates attempts; the database due time controls when a retry can run.

```sh
ddp jobs list --json
ddp jobs show job/sync_customers --json
ddp jobs run job/sync_customers --json
ddp runs list --job job/sync_customers --limit 20 --json
ddp runs show execution/<id> --json
ddp logs execution/<id> --limit 20 --json
ddp logs job/sync_customers --json
```

A workload failure can leave the execution `queued` for another attempt. Its
attempt is `failed`; the execution becomes `failed` after exhausting its limit.
A successful, validated result advances the watermark in the same transaction
that finishes the attempt and execution. Preparation errors also produce durable
attempt history. Cancellation terminates active work or stops a waiting retry and
leaves the execution `interrupted`.

Run and log lists default to 50 entries and accept limits from 1 to 100. They query
Postgres directly, including history for jobs removed from the registry. `runs show`
returns up to 100 recent attempts and `total_attempts`; compare the count with the
returned list to detect truncation. Stored output preserves the runner's redaction
and size limits. Run details include chain and parent execution IDs, skip reasons
and pending cancellation; the run, attempts and total count share one database
snapshot. Scheduled models also accept `model/<name>` in job views and run/log
filters. These commands work with the `ddp_readonly` database role.

## Side effects

`notify`, `export` and `operate` jobs must declare one idempotency strategy:
`provider_key`, `natural_key`, `reconcile` or `duplicates_acceptable`. The last
strategy also requires a reason. The job's own code must implement its declaration;
configuration cannot prove that an external service honors it.

```yaml
idempotency:
  strategy: duplicates_acceptable
  reason: A repeated daily digest is acceptable.
```

Starting a new manual execution requires confirmation that repeats the strategy:

```sh
ddp jobs run job/send_digest --confirm-idempotency duplicates_acceptable --json
```

Automatic retries retain effect keys. A new manual execution creates new keys.

## Scheduler

Run `ddp scheduler` with `DATABASE_URL` for the scheduler role and
`JOB_DATABASE_URL` for the restricted Python job role. `ddp scheduler status --json`
reads its heartbeat and advisory-lock ownership. Development leaves the scheduler
off until it is started explicitly.

Schedules use five cron fields in `ddp.timezone`. A missing daylight-saving time
produces no tick; a repeated local minute produces one. Planning saves elapsed
ticks, the complete dependency chain and its cursor in one transaction. Missed
ticks are skipped with `catchup: none`; `latest_only` queues the newest occurrence.
Long outages stream into Postgres without an arbitrary occurrence limit.

A scheduled job starts a chain. Jobs with `after` wait for their saved parent
execution IDs; a diamond waits for both parents. A failed or skipped parent stops
its descendants. Chains cannot join unrelated scheduled roots. Scheduled models
use the same queue and native Go refresh path under `model/<name>`.

`scheduler.max_workers` bounds active attempts. Job keys prevent overlap, and a
shared `concurrency_key` serializes jobs using the same integration. Postgres
session locks also cover independent manual CLI processes. Delayed retries release
worker slots while retaining their execution IDs and saved retry policy.

Shutdown stops planning, allows three seconds for active work, then interrupts the
remaining attempts. A new scheduler holding the advisory lock reconciles unfinished
work: remaining retries keep the execution ID and receive a new attempt ID;
exhausted executions fail. Recovery records an event transactionally. Manual and
operator-interrupted work is excluded from automatic scheduler recovery.

## Manual chains and backfills

`jobs run` executes only the named job by default. Add `--with-downstream` to
queue its complete chain for the scheduler and return immediately with the root
execution ID. Each selected side-effecting job requires its strategy to be repeated;
pass `--confirm-idempotency` more than once when the chain uses different strategies.

```sh
ddp jobs run job/sync_customers --with-downstream --json
ddp jobs backfill job/sync_customers \
  --from 2025-01-01T00:00:00Z --through 2025-01-01T02:00:00Z --json
```

Backfill defaults to a database-free preview. The endpoints are inclusive RFC3339
timestamps, the end must be historical, and the selected root must have a schedule.
The preview reports occurrences, total executions, selected jobs and required
idempotency strategies. Add `--with-downstream` to include each occurrence's chain.

To apply, repeat the same arguments with `--apply --confirm-executions <count>` and
any required `--confirm-idempotency <strategy>`. The count must match the preview.
All executions and the audit record commit together. Repeating an applied backfill
creates new executions and effect keys; it is an explicit historical rerun. It does
not move the automatic planner cursor or replace its ticks.

## Pause, resume and cancel

```sh
ddp jobs pause job/sync_customers --json
ddp jobs resume job/sync_customers --json
ddp runs cancel execution/<id> --json
```

Pause blocks future work and skips queued attempts; an active attempt can finish.
Paused downstream jobs stop their chains. Resume records elapsed paused ticks even
if the scheduler was offline, so they are not replayed later.

Cancel interrupts queued work immediately. For active work it saves a cancellation
request; the worker stops its workload before recording `interrupted`. Inspect the
run until that state appears. Cancellation prevents retries and downstream work,
including after a scheduler restart. It does not undo already committed effects.

Privileged job operations require database write authority. They record the database
session identity, action, target, time and result in `ddp.audit` in the transaction
that changes state. Missing confirmations and read-only mutation attempts return
exit code 4. The runtime also checks its lock connection while working and stops
an attempt if ownership is lost.

## Remaining scheduler work

`scheduler doctor`, the operational job catalog and platform system jobs remain
unfinished. General command capability declarations and auditing for other
privileged command families belong to the remaining operator-surface work.

Execution claims use a separate five-second transaction deadline. Once a claim
starts, parent cancellation waits for that transaction to finish before the attempt follows the
normal interrupted finalization path. This keeps cancellation during startup from
losing the attempt's durable outcome.
