# Persisted email

DDP stores messages in `ops.outbox` and attempts in `ops.deliveries`. Producers
call `comms.Enqueue` inside their existing Postgres transaction. Rollback removes
the message; replaying the same effect key and payload returns the existing ID.
Reusing that key for different content is refused. Group recipients and sender
are resolved when the message is enqueued.

## Configure SMTP

```yaml
comms:
  smtp:
    addr: smtp.example.test:587
    from: notifications@example.test
    username: notifications
    password_env: SMTP_PASSWORD
    tls: starttls
  groups:
    owners:
      recipients: [owner@example.test]
```

Set the named password environment variable on the relay. `starttls` requires
STARTTLS; `implicit` establishes TLS immediately. Both verify certificates.
`none` is restricted to an actual loopback connection without authentication,
for local SMTP fixtures. Each exchange has a 30-second deadline.

## Preview and send

Templates live in `templates/comms/<name>.txt` and optional `.html` files. The
text file defines Go templates named `subject` and `body`; the HTML file is an
ordinary Go HTML template. Missing required context fails rendering, and HTML
values are escaped. Subject length is at most 200 bytes. Template inputs are
limited to 256 KiB each; combined rendered content and encoded SMTP messages
have separate 1 MiB ceilings.

| Template | Context |
|---|---|
| `alert` | `Severity`, `Title`, `Message`, `OccurredAt`; optional `Details` |
| `digest` | `Date`, `Items` with `Title`, `Status`, `URL` |
| `password-reset` | `Name`, `ResetURL`, `ExpiresIn` |
| `welcome` | `Name`, `LoginURL` |

```sh
ddp comms preview welcome --data context.json --json
ddp comms enqueue --file message.json --json
ddp comms test-send --file message.json --json
ddp comms relay --once --json
```

`context.json` contains the template context alone. `message.json` includes:

```json
{
  "effect_key": "welcome/example-operator/v1",
  "template": "welcome",
  "recipients": ["group/owners"],
  "context": {"Name": "Example Operator", "LoginURL": "https://portal.example.test"}
}
```

`test-send` persists the message and attempts only that message. Inspect its
returned status: an unsuccessful SMTP attempt remains pending until its retry is
due. `relay` without `--once` polls continuously for explicit troubleshooting.
Delivery remains off in `ddp dev`.

## Scheduled delivery

`ddp scheduler` registers `ddp:comms_relay`, tagged `system`, and schedules it
every minute when SMTP is configured. Each durable execution drains up to 100 due
messages serially within a 50-second budget, reserving time before each additional
claim. The outbox owns message retries; scheduler polls have one attempt. A
successful poll can include pending or failed messages: inspect its result counts
and `comms show` for delivery outcomes. Database or configuration errors fail the
execution.

```sh
ddp jobs show ddp:comms_relay --json
ddp jobs pause ddp:comms_relay
ddp jobs resume ddp:comms_relay
ddp jobs run ddp:comms_relay --confirm-idempotency duplicates_acceptable
ddp runs list --job ddp:comms_relay --json
ddp logs ddp:comms_relay --json
```

Pause prevents new executions; cancel an active run with `ddp runs cancel
execution/<id>`. Resume does not replay skipped ticks. Historical backfills are
refused because message retry timing belongs to the outbox. Manual runs use the
same execution path and locks. Removing SMTP disables the schedule while preserving
job inspection; any already queued poll records a configuration failure. Explicit
`comms relay` and `test-send` remain direct operator actions and do not use the job
pause override. Health transition producers now use this outbox; see [health procedures](health.md).
Operational retention remains under construction.

## Retry and inspect

```sh
ddp comms list --json
ddp comms show message/<id> --json
ddp comms retry message/<id>
ddp comms resend message/<id> --effect-key resend/example/v1 --confirm
```

Rendering is saved before SMTP starts. Retries reuse that content, even after
source templates change. SMTP failures retry after 30 seconds, then double the
delay up to one hour; the initial budget is five attempts. Interrupted attempts
are recovered after the previous relay loses its database lock. A render failure
requires manual retry; retrying a failed message grants five more attempts.
Resending a delivered message copies its saved content under a new effect key.
These operator changes are audited in their state-changing transaction.

SMTP acceptance is the delivery boundary. If the acknowledgement is lost, a retry
can deliver a duplicate. Metadata inspection excludes message bodies, context and
addresses. The read-only database role cannot read those columns or mutate messages;
producer roles cannot forge delivery state.

## Content retention

Hourly platform cleanup expires old delivered and finally failed message content
under `retention.messages` (default `720h`). Pending and delivering messages are
preserved. `comms list|show` exposes `content_expired_at`; retry and resend refuse
expired content. Effect keys, content hashes and delivery metadata remain for
idempotency and investigation. See [operational retention](retention.md).
