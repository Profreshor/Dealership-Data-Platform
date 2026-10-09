from collections.abc import Iterator, Sequence
from pathlib import Path

import psycopg
import pytest

from ddp import JobContext, db
from ddp.context import WriteRef


def context(*writes: WriteRef) -> JobContext:
    return JobContext.from_dict({
        "execution_id": "e1", "attempt_id": "a1", "scheduled_at": "2026-01-01T00:00:00Z",
        "timezone": "UTC", "reads": [], "writes": [
            {"target": write.target, "mode": write.mode, "key": write.key} for write in writes
        ], "settings": {}, "integrations": {}, "watermarks": {},
    })


def test_copy_and_sql_share_the_explicit_transaction(
    ddp_db: psycopg.Connection[object], tmp_path: Path,
) -> None:
    ddp_db.execute('CREATE TABLE app.bulk (id integer PRIMARY KEY, "display name" text)')
    sql_file = tmp_path / "update.sql"
    sql_file.write_text('UPDATE app.bulk SET "display name" = %s WHERE id = %s', encoding="utf-8")
    with db.transaction(context()) as connection:
        assert db.copy_from(connection, "app.bulk", iter([(1, "café"), (2, None)]),
                            columns=["id", "display name"]) == 2
        db.execute_file(connection, sql_file, ("after", 1))
        assert ddp_db.execute("SELECT count(*) FROM app.bulk").fetchone() == (0,)
    assert ddp_db.execute('SELECT * FROM app.bulk ORDER BY id').fetchall() == [
        (1, "after"), (2, None),
    ]

    def broken_rows() -> Iterator[Sequence[object]]:
        yield (3, "uncommitted")
        raise RuntimeError("source stopped")

    with (
        pytest.raises(RuntimeError, match="source stopped"),
        db.transaction(context()) as connection,
    ):
        db.execute(connection, "DELETE FROM app.bulk")
        db.copy_from(connection, "app.bulk", broken_rows(), columns=["id", "display name"])
    assert ddp_db.execute("SELECT count(*) FROM app.bulk").fetchone() == (2,)
    with pytest.raises(ValueError, match="active transaction"):
        db.copy_from(ddp_db, "app.bulk", [], columns=["id"])
    with db.transaction(context()) as connection:
        with pytest.raises(ValueError, match="distinct"):
            db.copy_from(connection, "app.bulk", [], columns=["id", "id"])
        with pytest.raises(ValueError, match="schema.table"):
            db.copy_from(connection, "bulk", [], columns=["id"])
