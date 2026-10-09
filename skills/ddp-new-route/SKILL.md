---
name: ddp-new-route
description: Add a dealership-owned Go HTTP route with an explicit server policy to a DDP repository.
---

Use a declarative endpoint for a contracted table workflow. Use a custom Go route
when the workflow needs its own handler, validation or transaction.

1. Read [Client Go routes](../../docs/client-routes.md) and [Reporting endpoints](../../docs/reporting.md).
   Inspect
   `ddp capabilities --json`, `ddp routes --json` and `ddp registry --json`;
   declare any required permission in `ddp.yaml` first.
2. For a contracted table workflow, prepare an endpoint definition with its
   relation, `/api/` path, columns, key, filters, sort, search and policy. Review
   `ddp new endpoint <name> --definition <file> --dry-run --json`, then apply it
   without `--dry-run`; run `ddp validate` and `ddp routes --json`.
3. For a custom workflow, prepare a route definition with an explicit HTTP method
   and `/api/` path plus `public`, `authenticated`, `admin` or
   `permission:<name>` policy. Review `ddp new route <name> --definition <file>
   --dry-run --json`, then apply it without `--dry-run`. Route scaffolds reject
   `--source`.
4. Implement the generated feature under `internal/app/<feature>/`. Register it
   through the generated direct call in `internal/app/register.go`; use bounded
   request contexts, `httpx`, the existing API pool and server-side policy checks.
5. Rebuild the binary, then run `ddp config validate`, `ddp validate` and
   `ddp routes --json`. Test direct access to the route independently of frontend
   navigation and guards.
6. Run `ddp check --changed --json`; fix every selected check. Before opening a
   human-reviewed PR, run `ddp check --json` and include the result.

Keep business SQL and types inside the feature. Registration must compose without
database work, and the route ID remains stable when its implementation changes.
