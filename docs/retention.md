# Operational retention

`ddp:cleanup` runs hourly through the scheduler. It expires platform evidence and
expired login sessions. It never deletes the dealership's business rows, watermarks, planner
state, execution identities, tick identities or audit records.

The optional top-level configuration uses positive Go durations:

```yaml
retention:
  run_logs: 720h
  health: 2160h
  messages: 720h
```

These defaults retain log payloads and message content for 30 days, and health
payloads for 90 days. Omitted fields use their defaults. `30d` is not a Go duration;
use `720h`. Session expiry follows the authentication expiry time.

## What expires

- Completed attempt stdout, stderr, result and error are cleared after `run_logs`.
  Attempts belonging to an active execution remain intact. IDs, status, timestamps
  and attempt numbers remain available; `logs`, `runs show` and `diagnose` expose
  `payload_expired_at` so an expired log is distinguishable from an empty log.
- Old completed model-refresh errors are cleared. Refresh identities and status
  remain available.
- Delivered or finally failed messages expire after `messages`, measured from
  `finished_at`. Cleanup clears template context, subject and both bodies, and sets
  `content_expired_at`. Pending and delivering messages remain intact.
- Unreferenced health evaluations older than `health` are deleted. Evaluations
  referenced by old alerts retain identity, state, severity and routing metadata,
  with their message replaced by an expiry marker. The current evaluation of each
  check and evaluations needed by pending alerts remain intact.
- Alert template context is cleared when its linked message content has expired.
- Expired sessions are deleted through a narrow database function. The scheduler
  can call it without reading authentication tables or session hashes.

Execution, attempt, message, delivery and audit metadata remain for the deployment's
life. This preserves scheduler decisions, diagnostic failure state and message
idempotency. Sender and recipient metadata remain with message identities.

## Operate cleanup

```sh
ddp jobs show ddp:cleanup --json
ddp jobs run ddp:cleanup --json
ddp logs ddp:cleanup --json
ddp jobs pause ddp:cleanup
ddp jobs resume ddp:cleanup
```

A manual run applies the configured policy immediately. Historical backfill is
refused because cleanup operates on current database state. `ddp dev` leaves
scheduled cleanup off with the other scheduler jobs.

Each transaction selects at most 1,000 rows per category and skips locked rows.
Cleanup repeats batches within a 40-second budget, with three-second statement
timeouts. Counts describe committed changes; `more: true` means the time budget
stopped collection and another run may have work. A failed batch rolls back without
undoing earlier committed batches. Each batch records its counts in `ddp.audit`.

Expired message content cannot be retried or resent; those commands return refusal
exit code 4. Enqueuing the same content with its original effect key still returns
its existing identity, preserving duplicate protection. To send a new message,
provide fresh context and a new effect key. Expired failed messages remain visible
in history but no longer count as actionable failures in outbox health.
