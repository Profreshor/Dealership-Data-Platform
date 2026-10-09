# Synthetic onboarding discovery

This narrative accompanies `discovery.yaml` for the agent onboarding acceptance
exercise. It describes a disposable source and agreed reporting behavior. It does
not authorize connections to any dealership system. The exercise runner supplies the
local HTTP origin and synthetic API key separately; the `example.test` documentation
URL in the YAML names this fixture and is not a live documentation service.

## Source contract

`GET /customers` returns the current active customers as a JSON array. Authenticate
with the `X-API-Key` header using the environment variable `SYNTHETIC_API_KEY`.
Without the matching key, the source returns HTTP 401. It has no pagination,
redirects, writes or deletion feed. Each request may retrieve the complete small
snapshot; use a finite timeout. Treat a source error as a failed ingest.

Each record has three required string fields:

- `id`: stable, unique customer identifier;
- `name`: customer display name;
- `updated_at`: UTC RFC 3339 source update time.

The seeded source returns these two active customers:

| id | name | updated_at |
|---|---|---|
| one | Synthetic Customer | 2026-09-04T12:00:00Z |
| two | Example Customer | 2026-09-04T12:01:00Z |

All returned customers are active by the source contract; there is no separate
status field. Ignore deletions, as recorded in the structured discovery. Land the
full source record and upsert by `id`. Replaying a successful ingest must preserve
one row per customer. Advance the watermark to the greatest returned `updated_at`
only after a successful landing operation. Preserve the previous data and watermark
when the source fails.

## Reporting and operation

The Customers table page shows `id` and `name`, with one row for each landed active
customer. Read source-shaped data through staging, core and mart SQL models. Use a
staging view, a core materialized view and a mart view; the dependent refresh runs
after ingest. Schedule ingestion once per minute in the disposable proving ground.

Protect the report with a dedicated `customers.read` permission. Anonymous users
cannot read its API. Bootstrap the discovered synthetic operator for the smoke
exercise; inspect role permissions before giving any other user access. The report
must show both seeded rows after login.

This exercise's finish line is the initialized dealership repository's real `ddp smoke --json`,
plus replay and failed-source checks. Implement from these facts and DDP's public
documentation and action skills. Do not copy the existing proving-ground dealership
code, job, model or registry overlay. Naming new local artifacts is an implementation
choice; customer values, source semantics and reporting rules come from discovery.

The daily email and production recovery/access requirements in `discovery.yaml`
remain later onboarding work. Passing this exercise proves the local ingest-to-page
path; it does not certify email delivery or a complete production deployment.
