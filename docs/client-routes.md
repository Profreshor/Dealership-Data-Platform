# Client Go routes

Client routes are the dealership's own Go HTTP handlers, kept in its repository
under `internal/app/` beside the platform code. Scaffold one from a small YAML
definition:

```yaml
pattern: POST /api/customer-workflow
policy: permission:customers.manage
```

```sh
ddp new route customer_workflow --definition route.yaml --dry-run --json
ddp new route customer_workflow --definition route.yaml
```

The scaffold creates `internal/app/customer_workflow/register.go` and updates
`internal/app/register.go` to call it. The route stub returns HTTP 501 until its
workflow is implemented. Route scaffolding does not change `ddp.yaml`; the Go
registration is the runtime registry authority. `--source` is rejected for
routes. `--dry-run` reads the project and emits the same proposal shape without
creating files.

Declare permission names in `ddp.yaml` before scaffolding. Definitions accept only
`pattern` and `policy`: use an explicit HTTP method and `/api/` path, with `public`,
`authenticated`, `admin` or `permission:<name>`. The feature name must be a new Go
package name in lowercase with underscores. The aggregator must contain direct
feature `Register` calls; unusual composition logic requires manual registration.
Rebuild the binary before running `ddp validate` and `ddp routes --json` to check
the generated code and its conflicts with existing routes.

Use a declarative endpoint for reporting over a contracted relation. Use Go for a
workflow that needs its own handler. Keep its types, SQL and logic in
`internal/app/<feature>/`.

Each feature exposes `Register(reg *web.Registry)`:

```go
package customers

import (
  "net/http"

  "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
  "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)

func Register(reg *web.Registry) {
  reg.Handle("POST /api/customer-workflow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    // Validate input, use reg.Pool with r.Context(), and implement the workflow.
    httpx.Fail(w, http.StatusNotImplemented, "not_implemented", "Workflow is not implemented")
  }), web.Permission("customers.manage"))
}
```

For a manually created feature, declare the permission in `ddp.yaml`, grant
it through a role, and add its import and `customers.Register(reg)` call to
`internal/app/register.go`. The scaffold adds that call for you.
`cmd/ddp/main.go` needs no changes when adding a feature.

The registry's `Pool` is the existing API database pool. Its database role remains
the authority for SQL access. Keep queries inside request handlers and use bounded
request contexts and transactions where needed. Registration must do no database
work: `ddp routes --json`, `ddp validate` and `ddp config validate` compose routes
without a database, so `reg.Pool` is nil during those commands.

`api`, `dev`, `smoke` and `routes` use the same registration function. Client routes
receive the platform's session checks, typed policy, CSRF/origin checks, security
headers, request IDs, access logs and bounded metric labels. `web.Admin()` is for
operator accounts; dealership user administration uses explicit permissions. Use `httpx` for
response envelopes and decoding. Route conflicts, nil handlers and missing policies
fail composition.

Run `ddp validate`, inspect `ddp routes --json`, and add tests for direct access
before running `make check` and `make check-smoke`. Do not rely on frontend guards.
