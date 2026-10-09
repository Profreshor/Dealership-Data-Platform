from __future__ import annotations

import os
from collections.abc import Generator, Iterable, Sequence
from contextlib import contextmanager
from pathlib import Path

import psycopg
from psycopg import sql
from psycopg.abc import Params, QueryNoTemplate
from psycopg.pq import TransactionStatus

from .context import JobContext


@contextmanager
def transaction(ctx: JobContext) -> Generator[psycopg.Connection[object], None, None]:
    """Yield one explicit transaction using the runtime's DATABASE_URL."""
    url = os.environ.get("DATABASE_URL")
    if not url:
        raise RuntimeError("DATABASE_URL is required")
    with psycopg.connect(url, autocommit=True) as connection:
        with connection.transaction():
            yield connection


def execute(
    connection: psycopg.Connection[object], query: QueryNoTemplate, params: Params | None = None,
) -> psycopg.Cursor[object]:
    """Execute parameterized SQL in the caller's transaction; never commit it."""
    _require_transaction(connection)
    return connection.execute(query, params)


def execute_file(
    connection: psycopg.Connection[object], path: str | Path, params: Params | None = None,
) -> psycopg.Cursor[object]:
    """Execute a client-owned UTF-8 SQL file in the caller's transaction."""
    return execute(connection, Path(path).read_text(encoding="utf-8").encode("utf-8"), params)


def copy_from(
    connection: psycopg.Connection[object], table: str, rows: Iterable[Sequence[object]],
    *, columns: Sequence[str],
) -> int:
    """Stream Python rows through COPY FROM STDIN in the caller's transaction."""
    _require_transaction(connection)
    parts = table.split(".")
    if len(parts) != 2 or any(not part or "\x00" in part for part in parts):
        raise ValueError("copy_from requires a schema.table name")
    if (not columns or isinstance(columns, str) or len(set(columns)) != len(columns)
            or any(not column or "\x00" in column for column in columns)):
        raise ValueError("copy_from requires distinct, non-empty column names")
    statement = sql.SQL("COPY {} ({}) FROM STDIN").format(
        sql.Identifier(*parts), sql.SQL(", ").join(map(sql.Identifier, columns)),
    )
    count = 0
    with connection.cursor() as cursor, cursor.copy(statement) as copy:
        for row in rows:
            copy.write_row(row)
            count += 1
    return count


def _require_transaction(connection: psycopg.Connection[object]) -> None:
    if connection.info.transaction_status != TransactionStatus.INTRANS:
        raise ValueError("database helpers require an active transaction")
