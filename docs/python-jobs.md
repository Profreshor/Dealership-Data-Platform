# Python jobs

Keep each job's entrypoint in `jobs/` and the dealership's shared code, such as
integration wrappers, in the `client/` package.
The Go runner supplies `JobContext`, declared secret environment variables and
`DATABASE_URL`. Return a `JobResult` after the database work commits. Go records
the result and advances the watermark only after successful process exit.

## Transactions and bulk writes

Use one explicit transaction when several writes must succeed together:

```python
from ddp import JobContext, JobResult, db, job

@job
def run(ctx: JobContext) -> JobResult:
    with db.transaction(ctx) as connection:
        count = db.copy_from(
            connection, "erp.events", [(1, "received")], columns=["id", "state"],
        )
        db.execute(connection, "UPDATE erp.checkpoints SET value = %s", ("done",))
    return JobResult(rows_written=count)
```

`copy_from` quotes table and column identifiers and streams rows through
[Psycopg COPY](https://www.psycopg.org/psycopg3/docs/basic/copy.html).
`execute_file(connection, path, params)` reads a UTF-8 SQL file from the dealership's project.
These helpers require an active transaction and never commit it themselves.
Exceptions roll back the whole transaction. Parameters are values; use
`psycopg.sql.Identifier` when composing identifiers.

## Raw landing and replay

`landing.upsert`, `landing.append` and `landing.replace` each own one transaction.
The project's migration creates the table with `payload jsonb`, `_loaded_at`,
`_source_key` and its extracted key columns. Declare those columns and the write
mode in `ddp.yaml`.

Upsert uses the declared natural key to make replay safe. Append preserves insert
semantics; a declared unique key rejects duplicates. Replacement locks writers,
deletes the old rows and inserts the complete new batch in one transaction. An
iteration or insert failure preserves the previous batch. A successful empty
replacement clears the table. These operations need ordinary DML privileges.

Use `ctx.effect_key("operation-name")` for external effects that accept an
idempotency key. Retries share an execution's key; a new execution gets a new key.
Job code must follow the provider's replay guarantees.

## HTTP requests

Use `ddp.http.request("GET", url, timeout=5, auth_header=("X-API-Key", secret))`.
It returns a response with `status`, `headers` and a byte `body`. The calling code owns
JSON parsing, pagination and provider error meanings. The synthetic project's
`tests/proving-ground/client/` package demonstrates this boundary.

The helper rejects redirects, limits response bytes and reports errors without
request or response data. It retries connection failures, `429`, `500`, `502`,
`503` and `504` only for idempotent HTTP methods. There are at most three retries;
the default is two. `Retry-After` seconds and HTTP dates are supported. A requested
wait beyond the remaining 30-second retry budget fails back to the Go scheduler,
without sending an early retry. Every request requires an explicit finite timeout.

## Disposable database tests

Load the optional fixture in `tests/python/conftest.py`:

```python
pytest_plugins = ["ddp.testing"]
```

The `ddp_db` fixture returns an autocommit Psycopg connection to a fresh database
with `app`, `staging`, `core` and `mart` schemas. Create the job's own tables in the test.
It sets `DATABASE_URL` for the job under test, then drops the test database and
restores the environment. It reads only `TEST_DATABASE_URL`, which must point to
disposable Postgres with database-creation permission. Tests skip if it is unset.

Run `make check-python` for Ruff, strict Pyright, import boundaries and pytest.
