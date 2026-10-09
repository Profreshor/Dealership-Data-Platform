# Registry commands

`ddp.yaml` declares project resources and their relationships. The CLI can read
these facts without a database connection:

```sh
ddp registry --json
ddp search customers --json
ddp --config ../acme/ddp.yaml registry --json
```

`registry` returns supported types, configured resources and typed relationships.
Supported types remain visible when a project has no resources of that type.
`search` matches references, names, purposes and configuration without regard to
case. A search with no matches returns an empty array. Both commands validate the
registry and its entrypoint files before returning the versioned CLI envelope.

## Relationships

Each relationship starts at the resource that owns its declaration and ends at
the named resource. Its kind records the declaration: `reads`, `writes`, `after`,
`model`, `endpoint`, `permission`, `target` or `notify`.

For example, `job/sync_customers` has a `writes` relationship to
`table/synthetic.customers`. The table's `written_by` list is derived from that
declaration. It does not need a second list in YAML.

Each resource includes:

- `dependencies`: the references it declares, including write targets;
- `dependents`: resources that reference it;
- `read_by` and `written_by`: readers and writers derived from the relationship
  kinds;
- its identity, purpose and original configuration.

Lists are sorted and duplicate-free. A job may read and write the same table;
both relationship kinds remain present, while the table appears once in the
job's dependencies.

Validation rejects unknown references, incorrect reference types, graph cycles,
unknown permissions and conflicting owners of the same SQL relation. Endpoint
permission policies and page permissions participate in this same graph.

## Live inspection

Set `DATABASE_URL` to the intended database. Read-only credentials are sufficient:

```sh
ddp inspect job/sync_customers --json
ddp inspect model/mart.customers --json
ddp inspect service/scheduler --json
ddp tables list --json
ddp tables show table/erp.customers --json
ddp integrations show integration/erp --json
```

`inspect` combines the declaration and its derived relationships with available
job, execution, model or relation observations. Model history contains the newest
ten refreshes. Tables include actual columns, physical kind, row estimates, size
and direct view dependencies from Postgres. Declared source schemas participate
alongside the configured warehouse layers; missing declared relations report
`exists: false`. Dependencies use model references where the registry declares
models. These commands do not read business row contents.

Integration details derive jobs and landed tables from job declarations, then show
each relation and its latest successful writer finish time. This timestamp is an
execution observation, not a measured freshness verdict. Secrets appear by name.
Database errors fail inspection. Health expectations are declared facts; health
evaluation and available-action metadata remain under construction.

## Create registered artifacts

Provide a YAML mapping for the new resource's facts. References must already exist:

```yaml
# /tmp/check-records.yaml
purpose: Check the agreed source contract.
action: check
```

```sh
ddp new job check_records --definition /tmp/check-records.yaml --dry-run --json
ddp new job check_records --definition /tmp/check-records.yaml
```

The preview exposes proposed YAML and source as text. Application creates source
files, validates references and entrypoint paths, then atomically replaces the
registry. Existing artifacts and duplicate names are refused. Ordinary failures
remove files created by the command. A process killed before registry publication
can leave unregistered files; inspect and remove those files before retrying.

| Kind | Artifact and required input |
|---|---|
| `job check_records` | `jobs/check_records.py`; optional `--source`, otherwise an explicit unimplemented job body. A native model job needs only its declaration. |
| `model mart.customers` | `models/mart/customers.sql`; `--source` supplies SQL. |
| `integration erp` | `client/erp.py`; optional `--source` supplies the wrapper. |
| `endpoint customers` | Declarative endpoint in YAML. |
| `route customer_workflow` | New Go feature and aggregator call; definition supplies `pattern` and `policy`. See [client-routes.md](client-routes.md). |
| `page customers` | Table or system page in YAML; a [custom page](custom-pages.md) also creates its registered `.tsx` component. |
| `health customers_fresh` | Freshness declaration, or `health/customers_fresh.sql` with `kind: sql` and `--source`. |
| `migration 20260904120000_add_customers` | `migrations/app/20260904120000_add_customers.sql`; requires `--source`, leaves YAML unchanged. Rebuild the binary to embed it. |

Generated path fields may be omitted or must match the generated path exactly.
Implement generated Python stubs, then run `ddp validate` and `make check` before
opening a PR. Supplied code still needs the language and real Postgres checks;
scaffolding does not execute it or apply migrations.

Go route scaffolds are the one generated artifact that updates an existing source
file: `internal/app/register.go` is atomically rewritten to add the new feature
registration while preserving existing direct registrations and comments. The
feature directory must be absent. This route-specific aggregator
update is the exception to the general no-overwrite rule. Rebuild the binary,
then run `ddp validate` and `ddp routes` to discover the new Go registration;
an installed binary cannot load newly written Go code dynamically.
