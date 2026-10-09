# HTTP contract

HTTP JSON responses use `{ "ok": true, "data": ..., "error": null }` or
`{ "ok": false, "data": null, "error": { "code": "...", "message": "..." } }`.
The CLI adds its own envelope version; HTTP does not use the CLI wrapper.

## Authentication

- `POST /api/auth/login`: JSON `{email,password}`. Success returns the session
  shape below and sets an opaque `ddp_session` cookie. Invalid credentials return
  HTTP 401 without revealing whether the account exists.
- `GET /api/auth/session`: returns the current session, or HTTP 401.
- `POST /api/auth/logout`: requires `X-CSRF-Token`, deletes the session and returns
  `data: null`.

Session data is `{user:{id,email,admin,permissions:[]},csrf_token:"..."}`.
Permissions are loaded from current database grants on every request. Session
tokens are stored only as hashes. Cookies are HttpOnly and SameSite=Lax; HTTPS
uses Secure. Local development uses the configured localhost HTTP origin.
Unsafe cookie-authenticated requests require a matching CSRF token and reject a
foreign Origin. Login accepts only JSON and rejects foreign browser origins.

## Portal

- `GET /api/portal`: authenticated; returns `{display_name,pages:[]}` with navigation
  filtered by current permissions. Each page has its declared `label,path,kind`,
  and table pages include `endpoint`, `columns`, `filters`, `sort`, `search`,
  effective `page_size`, and `shape` (`list` or `singleton`). Declared exports add
  `{format:"csv",max_rows}` under `export`.
- Registered table endpoints return `{rows:[...],next_cursor:null|string}`.
  Permission policies are enforced independently of navigation.
- `/healthz` and `/readyz` expose only `{ok:boolean}`; `/readyz` returns 503 for
  unavailable Postgres or incomplete/drifting migrations.
  Applied migrations newer than this binary's latest timestamp in each ledger
  permit image rollback. Unknown older IDs, same-timestamp replacements and
  malformed IDs still fail. Readiness checks migration history; release tests must
  prove that forward SQL remains compatible with the previous image.

See [accounts.md](accounts.md) for invitation and recovery,
[reporting.md](reporting.md) for table operations, and
[system-console.md](system-console.md) for operator routes.

The embedded portal revalidates documents and unversioned files with `no-cache`.
Existing files under the generated `assets/` path with content-hash filenames use
one-year immutable caching. Missing assets return a 404 without immutable caching; extensionless
application paths serve the revalidated SPA document. GET and HEAD share headers,
and HEAD does not send the document body.
