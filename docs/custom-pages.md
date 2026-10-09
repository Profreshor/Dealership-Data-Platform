# Custom portal pages

Use a table page for ordinary reporting. Use a custom page when the dealership needs its
own layout or interaction. SQL and protected endpoints still own business results
and data access.

Declare the required permission first, then scaffold the page:

```yaml
# /tmp/dashboard.yaml
label: Dashboard
path: /dashboard
kind: custom
permission: customers.read
order: 2
```

```sh
ddp new page dashboard --definition /tmp/dashboard.yaml --dry-run --json
ddp new page dashboard --definition /tmp/dashboard.yaml
```

The command creates `frontend/apps/portal/src/routes/dashboard.tsx` and updates
`ddp.yaml` together. It refuses existing files and duplicate page IDs. The initial
page states that it is not ready yet. Implement its component before release;
custom-page scaffolding does not accept `--source`.

Each page module uses a named import from `@ddp/ui` and a direct registration:

```tsx
import { registerPage } from "@ddp/ui";

function DashboardPage() {
  return <section><h1>Dashboard</h1></section>;
}

registerPage("dashboard", DashboardPage);
```

Keep registrations at the top level of `.tsx` modules under `src/routes/`, with a
literal YAML page ID and a component identifier. Nested directories are supported;
Hidden paths, `node_modules` and `.test.tsx` modules are excluded. Helpers may live in separate files. Dynamic
registrations, namespace imports from `@ddp/ui`, re-exported registration helpers
and symlinked route files are refused by the build checks.

`npm run build --prefix frontend` checks types and compares registrations with
`ddp.yaml`. It rejects missing, unknown, non-custom and duplicate IDs. Labels and
paths can change while the YAML ID remains stable. A custom page at `/` replaces
the default workspace home. Vite reloads the page registration set after changes;
restart `ddp dev` after changing YAML so the API also reads the new configuration.

The shell renders a custom component only when the server includes its page in the
current user's permitted metadata. Unknown or unavailable pages show the existing
unavailable state. Component failures retain the shell and offer retry. Use the
shared API client and TanStack Query for data; every endpoint must enforce its own
server policy independently of navigation.

Run `ddp validate`, `make check` and `make check-smoke` before a PR. The custom-page
smoke check scaffolds an isolated synthetic project, proves build mismatch failures,
and tests its embedded portal against disposable Postgres.
