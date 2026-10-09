# System console

Operators can open **System** in the portal to inspect scheduler status,
saved health checks, recent failures, jobs, runs and attempt logs. The template
registers this page in `ddp.yaml`:

```yaml
pages:
  system:
    label: System
    path: /system
    kind: system
    policy: admin
    order: 100
```

System pages require `policy: admin`. Role permissions, including `users.manage`,
do not grant access. Each system API route checks the current session's operator
flag on the server. Navigation also excludes the page from other users.

The console reads the same inspection functions as `ddp status`, `ddp runs` and
`ddp logs`. Refresh retrieves saved facts; it does not execute health checks or
jobs. Health observation status distinguishes current, stale and unobserved data.
Use the CLI to investigate or take operational actions.

| Protected endpoint | Data |
|---|---|
| `GET /api/system/status` | Shared status, jobs, health, outbox counts and latest 20 failures |
| `GET /api/system/runs` | Latest 50 executions |
| `GET /api/system/runs/{id}/logs` | Latest 100 attempts for one execution |

Requests have a ten-second query deadline and return the standard API envelope.
Responses are not cached. Logs render as text, and expired payloads retain their
expiry marker without revealing removed content. The console does not expose
email bodies, arbitrary SQL, or operational write actions.

`make check-smoke` runs Playwright against the embedded portal, real API and a
disposable Postgres database. The console fixture covers operator navigation,
saved health and failure evidence, logs, table access, direct API refusal for a
user manager without the operator flag, and desktop and mobile layouts.
