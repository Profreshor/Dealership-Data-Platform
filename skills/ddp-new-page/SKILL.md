---
name: ddp-new-page
description: Add a declarative or custom React portal page registered in a DDP repository.
---

Use a table page for ordinary reporting. Use a custom page when the dealership needs a
distinct layout or interaction; keep business results in protected server
endpoints.

1. Read [Custom portal pages](../../docs/custom-pages.md). Inspect
   `ddp capabilities --json`, `ddp registry --json` and `ddp routes --json`;
   declare the required permission in `ddp.yaml` first.
2. Prepare the page definition with its stable ID, label, path, kind, permission
   and order. Review `ddp new page <name> --definition <file> --dry-run --json`,
   then apply it without `--dry-run`. Page scaffolds reject `--source`.
3. For a custom page, implement the generated `.tsx` module under
   `frontend/apps/portal/src/routes/`, using a named `@ddp/ui` import and a
   top-level literal `registerPage` call. Use the shared API client and TanStack
   Query; enforce authorization in every endpoint.
4. Run `ddp config validate`, `ddp validate`, `npm run build --prefix frontend`
   and inspect the page's declared route and permission. Use synthetic development
   data and restart `ddp dev` after YAML changes.
5. Run `ddp check --changed --json`; fix every selected check. Before opening a
   human-reviewed PR, run `ddp check --json` and include the result.

Keep page IDs stable and avoid putting credentials, business definitions or data
access rules in frontend code.
