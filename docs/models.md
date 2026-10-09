# SQL model commands

Declare each model in `ddp.yaml` and place its single `SELECT` under
`models/<layer>/`. Use a development database with current migrations:

```sh
ddp models plan --json
ddp models apply --json
ddp models verify --json
ddp models refresh model/core.customers --json
ddp jobs run job/refresh_customers --json
```

`plan` reads catalogs in a read-only transaction. It lists models in dependency
order with their current materialization and SQL beside the proposed file contents.
A missing relation has empty `current_materialization` and null `current_sql`.
Postgres formats stored SQL, so text differences alone do not prove a query changed.
Planning does not execute or validate the proposed SQL.

`apply` creates or replaces each relation in its own transaction, verifies its
contract and direct PostgreSQL dependencies, and records the outcome in
`ops.model_refreshes`. The first failure stops downstream work. `verify` checks
existing tables and models, including data nullability and key uniqueness.

`refresh` applies one registered model; upstream relations must already exist.
It checks columns, types, key columns, materialization and dependencies. Data scans
for nulls and duplicate identities belong to explicit verification and health
checks. Declare a job with `model: model/core.customers` to run it through the
standard execution and attempt history, timeout and cancellation handling. These
jobs run in Go using the scheduler role and require no Python job credentials.

An unchanged materialized definition uses PostgreSQL's ordinary transactional
refresh. It preserves the relation and its dependents; readers may wait for the
refresh lock. A query failure restores the previous cached rows. Changed leaf
definitions are replaced transactionally. PostgreSQL rejects replacement when
dependents exist; coordinated graph replacement remains unfinished. DDP never
uses an implicit cascading drop. Materialized models require a declared
`unique_key`, which describes business identity rather than a physical constraint.
