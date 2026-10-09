---
name: ddp-new-integration
description: Add a declared external integration and its bounded wrapper module to a DDP repository.
---

Create an integration only after discovery records the external system,
authentication, limits, pagination and data semantics. Store secret names in the
registry and values in the approved development environment.

1. Read [Registry commands](../../docs/registry.md) and [Python jobs](../../docs/python-jobs.md).
   Inspect `ddp capabilities --json`, `ddp registry --json` and
   `ddp integrations list --json`.
2. Prepare the integration definition with its real kind, base URL, docs and
   secret name. Review `ddp new integration <name> --definition <file> --source <file> --dry-run --json`,
   then apply it without `--dry-run`.
3. Implement the generated wrapper module in the dealership's `client/` package. Give each consuming job
   explicit `reads: [integration/<name>]` and its actual write contract. Use
   synthetic endpoints and development secrets while testing.
4. Run `ddp config validate`, `ddp validate`, `ddp integrations show integration/<name> --json`
   and `ddp registry --json`. Confirm that no secret value entered YAML, source,
   logs or command arguments.
5. Run `ddp check --changed --json`; fix every selected check. Before opening a
   human-reviewed PR, run `ddp check --json` and include the result.

Do not create a generic connector or claim guarantees the external system does not
provide. Put provider-specific behavior in the named wrapper module.
