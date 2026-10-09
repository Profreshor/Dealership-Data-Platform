---
name: ddp-new-model
description: Add a registered SQL model with a contracted relation to a dealership's DDP repository.
---

Create one model whose SQL and output contract are agreed. Keep one `SELECT` in
the model file; declare its inputs, layer, materialization, refresh policy and
contract in `ddp.yaml`.

1. Read [SQL model commands](../../docs/models.md), then inspect facts with
   `ddp capabilities --json`, `ddp registry --json` and `ddp tables list --json`.
2. Prepare the model definition and SQL source. Use
   `ddp new model <layer.name> --definition <file> --source <file> --dry-run --json`
   to review the proposed files and registry, then apply it without `--dry-run`.
3. Run `ddp config validate`, `ddp validate` and `ddp models plan --json`
   against synthetic development Postgres. Resolve undeclared inputs, layer
   violations and contract failures. Review the plan, then run
   `ddp models apply --json` and `ddp models verify --json` to create and check
   the relation.
4. Run `ddp check --changed --json`; fix every selected check. Before opening a
   human-reviewed PR, run `ddp check --json` and include the result.

Apply only the reviewed plan in synthetic development Postgres. Keep joins and
calculations in SQL, and keep the registry limited to stable declared facts.
