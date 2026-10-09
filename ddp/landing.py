from __future__ import annotations

import json
import re
from collections.abc import Iterable, Mapping
from datetime import UTC, datetime
from typing import Any, cast

from psycopg import Connection, sql

from .context import JobContext
from .db import transaction

_IDENTIFIER = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


def upsert(
    ctx: JobContext,
    integration: str,
    entity: str,
    rows: Iterable[Mapping[str, Any]],
    *,
    key: str | list[str],
) -> int:
    return _write(ctx, integration, entity, rows, key=key, mode="upsert")


def append(
    ctx: JobContext,
    integration: str,
    entity: str,
    rows: Iterable[Mapping[str, Any]],
    *,
    key: str | list[str] | None = None,
) -> int:
    return _write(ctx, integration, entity, rows, key=key, mode="append")


def replace(
    ctx: JobContext,
    integration: str,
    entity: str,
    rows: Iterable[Mapping[str, Any]],
    *,
    key: str | list[str],
) -> int:
    key = _normalise_key(key)
    materialized = list(rows)
    with transaction(ctx) as connection:
        _table(connection, integration, entity)
        connection.execute(
            sql.SQL("LOCK TABLE {}.{} IN SHARE ROW EXCLUSIVE MODE").format(
                sql.Identifier(integration), sql.Identifier(entity)
            )
        )
        connection.execute(
            sql.SQL("DELETE FROM {}.{}").format(
                sql.Identifier(integration), sql.Identifier(entity)
            )
        )
        return _insert(connection, integration, entity, materialized, key, "append")


def _write(
    ctx: JobContext,
    integration: str,
    entity: str,
    rows: Iterable[Mapping[str, Any]],
    *,
    key: str | list[str] | None,
    mode: str,
) -> int:
    if key is not None:
        key = _normalise_key(key)
    elif mode == "append":
        key = _declared_key(ctx, integration, entity)
    materialized = list(rows)
    with transaction(ctx) as connection:
        _table(connection, integration, entity)
        return _insert(connection, integration, entity, materialized, key, mode)


def _insert(
    connection: Connection[object],
    integration: str,
    entity: str,
    rows: list[Mapping[str, Any]],
    key: str | list[str] | None,
    mode: str,
) -> int:
    if not rows:
        return 0
    keys = [key] if isinstance(key, str) else key
    columns = ["payload", "_loaded_at", "_source_key"] + (keys or [])
    placeholders = sql.SQL(", ").join(sql.Placeholder() for _ in columns)
    statement = sql.SQL("INSERT INTO {}.{} ({}) VALUES ({})").format(
        sql.Identifier(integration), sql.Identifier(entity),
        sql.SQL(", ").join(map(sql.Identifier, columns)), placeholders,
    )
    if mode == "upsert":
        if not keys:
            raise ValueError("upsert requires key")
        statement += sql.SQL(
            " ON CONFLICT ({}) DO UPDATE SET payload = EXCLUDED.payload, "
            "_loaded_at = EXCLUDED._loaded_at, _source_key = EXCLUDED._source_key"
        ).format(sql.SQL(", ").join(map(sql.Identifier, keys)))
    loaded_at = datetime.now(UTC)
    values = [
        (json.dumps(dict(row)), loaded_at, _source_key(row, keys),
         *(row.get(name) for name in keys or []))
        for row in rows
    ]
    with connection.cursor() as cursor:
        cursor.executemany(statement, values)
    return len(rows)


def _declared_key(ctx: JobContext, integration: str, entity: str) -> str | list[str] | None:
    targets = {f"{integration}.{entity}", f"table/{integration}.{entity}"}
    for write in ctx.writes:
        if write.target in targets and write.key is not None:
            return write.key
    return None


def _normalise_key(key: str | list[str]) -> str | list[str]:
    keys = [key] if isinstance(key, str) else key
    if not keys or any(
        not isinstance(item, str) or not item.strip() for item in cast(list[object], keys)
    ):
        raise ValueError("landing key must contain non-empty column names")
    return key


def _source_key(row: Mapping[str, Any], keys: list[str] | None) -> str:
    if not keys:
        return json.dumps(dict(row), sort_keys=True, default=str)
    return json.dumps({key: row.get(key) for key in keys}, sort_keys=True, default=str)


def _table(connection: Connection[object], integration: str, entity: str) -> None:
    if not _IDENTIFIER.fullmatch(integration) or not _IDENTIFIER.fullmatch(entity):
        raise ValueError("landing identifiers must be simple SQL names")
    found = connection.execute(
        "SELECT 1 FROM information_schema.tables "
        "WHERE table_schema = %s AND table_name = %s",
        (integration, entity),
    ).fetchone()
    if found is None:
        raise ValueError(f"landing table does not exist: {integration}.{entity}")
