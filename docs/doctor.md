# Doctor

`ddp doctor --json` checks the operator's current environment and live database.
It returns one versioned envelope containing an observation time, overall `state`
and checks with stable references, severity, message and a concrete `repair` for
each problem. Exit 0 means every applicable check passed; exit 3 means at least one
check is failing or unknown. Envelope `ok` means the report was produced; inspect
`data.state` for health. Invalid command usage remains exit 2.

```sh
ddp doctor
ddp doctor --config /opt/ddp/ddp.yaml --json
```

Doctor does not apply repairs, migrate the database, fetch Git updates, evaluate
business rules, write health history or send mail. Database checks work with the
read-only role. Run host checks as an authorized host administrator: a process or
container without Docker access cannot establish host health.

## Checks and repairs

| Check | Evidence |
|---|---|
| Configuration | The same registry and referenced-file validation as `ddp validate`; invalid configuration is reported without echoing its contents. |
| Environment | `.env` beside the registry must be a regular file with mode `0600`. Doctor checks metadata, rejects symlinks and never reads the file. Required platform, integration and SMTP secret names must be present in the process environment; only missing names are printed. |
| Docker | A read-only `docker info` request reaches the daemon. |
| Serving port | A local configured port is available for startup, or its `/readyz` returns HTTP 200 and `{"ok":true}`. A free port proves availability, not a running API. Redirects and proxies are disabled. |
| Template | Local platform files match `ddp.template_revision`, including untracked additions. Intentional changes still report drift for upgrade review. Missing Git history is unknown; this does not discover newer releases. |
| SMTP | Greeting, configured TLS negotiation and NOOP succeed. No AUTH, sender, recipient or message commands are issued. Credentials and deliverability remain untested. Unconfigured SMTP is not applicable. |
| Database, scheduler, disk, outbox | The exact checks used by continuous [platform health](health.md), read live without recording alerts. |
| Migrations | All embedded migrations are applied with matching checksums. Unknown applied IDs at or before the latest embedded timestamp in their ledger, and malformed IDs, fail. Later migrations permit image rollback. |
| Clock | Database time lies within five seconds of the operator's query interval. Query time is accounted for; this checks relative skew, not absolute NTP correctness. |

Template comparison covers `cmd/ddp`, `internal/ddp`, `ddp`, `migrations/ddp`,
`frontend/packages/ddp-ui`, `deploy`, `bin`, `AGENTS.md`, `CLAUDE.md`, `skills`,
`.agents/skills` and `.claude/skills`. Shared skill changes, including intentional
additions by the dealership, are reported for review. The dealership's own
configuration, application code, models and migrations are outside this comparison.

Network and process probes have three-second limits; the report has a 45-second
context budget. Host facts describe the environment running doctor. They do not
prove Cloudflare reachability, a separate database volume's capacity or current
off-host storage contents. Doctor checks persisted backup and restore evidence when
configured. Missing `.env`, Git history or Docker access in an application image is
reported as unavailable evidence, not concealed as healthy.
