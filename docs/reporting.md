# Reporting endpoints

Declare a contracted relation and the operations callers may use. Joins and
calculations stay in SQL models:

```yaml
endpoints:
  customers:
    reads: [model/mart.customers]
    path: /api/customers
    policy: permission:customers.read
    columns: [id, name, city]
    unique_key: [id]
    filters: [city]
    sort: [name]
    search: [name]
    page_size: 50
    export: { format: csv, max_rows: 50000 }
```

Every operation uses the declared server policy and checks current permissions.
`ddp routes --json` lists the actual declarative and platform registrations and
their policies without connecting to a database. Route patterns must be literal,
and conflicting registrations fail startup. The dealership's own workflow handlers register through the
[client composition root](client-routes.md). Use `ddp new route` to generate a
501 stub and add its registration to the composition root; implement the handler,
rebuild the binary, then run `ddp validate` and `ddp routes`.

## List, filter and search

```text
GET /api/customers?limit=25&filter.city=Chicago&q=smith&sort=-name
```

The response uses the shared `{ok, data, error}` envelope. `data` contains `rows`
and a nullable `next_cursor`. Only declared columns are returned. PostgreSQL
decimal values cross the JSON boundary as strings to preserve precision.

- `limit` is an integer from 1 to 1000; the default is `page_size`, or 50.
- `filter.<column>` performs equality on a declared filter column using a bound
  value. Multiple filter columns combine with AND.
- `q` performs case-insensitive literal substring matching across declared search
  columns. `%`, `_` and backslashes are ordinary search characters.
- `sort` is a comma-separated list of allowed sort columns. Prefix a column with
  `-` for descending order. The configured sort is the default.

Unknown and repeated parameters, disallowed columns and invalid typed values
return HTTP 400. Every database query has a five-second deadline; timeout returns
504 for JSON responses. Query errors do not disclose SQL or database details.

## Pagination and lookup

The endpoint's key must include a primary or unique key from the relation contract;
its columns must be non-nullable and returned. Sorts append missing key columns to
break ties. Nulls sort last in either direction, using explicit PostgreSQL
[`NULLS LAST`](https://www.postgresql.org/docs/17/queries-order.html).

Send `next_cursor` as the next request's `cursor`, keeping the same filters, search
and sort. Page size may change. Cursors preserve exact SQL text values and are
bound to the endpoint declaration and query; they do not grant access. Tokens
larger than 16 KiB are refused. Each page sees current data, so concurrent changes
to sort values can move records between pages; pagination does not hold a database
snapshot across requests.

```text
GET /api/customers/rows/customer-123
GET /api/customers/row?key.id=customer-123
GET /api/assignments/row?key.employee_id=123&key.shift_id=456
```

Path lookup applies to a single-column key. Query lookup requires every key
component exactly once. Missing rows return 404; multiple matches fail the identity
contract. `shape: singleton` instead returns one row directly in `data`, accepts
no pagination or sorting, returns 404 for no row and fails if more than one exists.

## CSV export

```text
GET /api/customers/export.csv?filter.city=Chicago&sort=name
```

Export is registered only when declared. It accepts the same filters, search and
sort, streams at most `max_rows`, and rejects client `limit` and `cursor` values.
The response is a CSV attachment with declared column headers and PostgreSQL text
values; null fields are empty. Values beginning with spreadsheet formula markers
or control prefixes receive an apostrophe so spreadsheets treat them as text.

`X-DDP-Export-Limit` reports the ceiling. After reading the complete body, HTTP
trailers `X-DDP-Export-Rows` and `X-DDP-Export-Truncated` report the count and
whether another row existed. A query or write failure aborts the response; clients
must treat an interrupted download as incomplete.

## Portal table controls

A YAML page with `kind: table` uses its endpoint's declared columns, filters, sort,
search, page size and export limit. The shared `EndpointTable` renders the controls;
no separate TypeScript page is needed.

Enter search text or enable exact filters, then choose **Apply**. An enabled filter
with an empty input matches an empty string. **Reset** restores the endpoint's
configured order and removes search and filters. Choosing a sort column makes it
the primary order and retains the other configured sort terms.

**Next** and **Previous** use a cursor history. Applying criteria starts at page one.
The row count describes the displayed page; it is not a total. Singleton endpoints
show one record without pagination, or an empty state when no record exists.

**Download CSV** uses the applied search, filters and sort, independently of the
current page. The displayed export ceiling applies even when more rows match.
The browser waits for a complete CSV response before downloading it and shows
request failures on the page. It does not use HTTP trailers to claim the export
contains every matching row.

## Request protection and logs

Every response receives a fresh `X-Request-ID`; supplied IDs are ignored. The
server writes JSON access logs with the same ID, registered route pattern, method,
status, response bytes and duration. Raw paths, query values, credentials, cookies
and panic messages are excluded. An abort before headers has status `0` and
`aborted: true`; failures after headers abort the transfer rather than append an
error body. API responses use `Cache-Control: no-store`.

Unsafe `/api/auth/` requests share a process-wide limit of four active requests
and a fixed one-minute budget of 60 attempts per actual network peer. Forwarding
headers are ignored; clients behind the same proxy or NAT share that budget.
The peer map retains at most 4096 identities and refuses new identities until
entries expire. Rejection returns 429 with `Retry-After`, before reading the body
or hashing a password. The separate login budget allows ten attempts per normalized
email per minute. Limits are process-local and reset on restart.

JSON request bodies accept exactly one value and at most 1 MiB, including trailing
whitespace. `ddp smoke --json` sends access logs to stderr and reserves stdout for
its result envelope.
