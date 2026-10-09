---
name: ddp-new-job
description: Add a registered Python or native model job to a dealership's DDP repository.
---

Create one job from agreed facts. Keep the job's purpose, action, reads, writes,
schedule, timeout, retry and idempotency strategy in `ddp.yaml`; keep behavior in
the generated source and the standard runner.

1. Read [Python jobs](../../docs/python-jobs.md), then inspect the current registry
   with `ddp capabilities --json`, `ddp registry --json` and `ddp jobs list --json`.
2. Choose a new lowercase snake case name. Prepare a YAML mapping containing only
   the job facts. Use `ddp new job <name> --definition <file> --dry-run --json` and
   review the proposed registry and files.
3. Apply the scaffold with `ddp new job <name> --definition <file>`. Supply
   `--source <file>` only when the job source is ready; implement the generated
   Python entrypoint under `jobs/` (or use the declared native model job).
4. Run `ddp config validate`, `ddp validate`, `ddp registry --json` and
   `ddp jobs show job/<name> --json`. Check references, contracts and effects
   against the agreed facts. Use synthetic development data and credentials. Run
   the job through its real failure and replay path, then inspect it with
   `ddp runs list --job job/<name> --json` and `ddp logs job/<name> --json`;
   confirm a successful replay preserves the declared idempotency and watermark
   behavior.
5. Run `ddp check --changed --json`; fix every selected check. Before opening a
   human-reviewed PR, run `ddp check --json` and include the result.

Do not invent schedules, integrations, destinations, credentials or idempotency
semantics. A job that sends, exports or operates must have its real strategy before
it is enabled.
