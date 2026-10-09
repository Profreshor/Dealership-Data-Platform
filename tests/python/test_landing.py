import os
import uuid
from collections.abc import Iterator, Mapping

import psycopg
import pytest
from psycopg import sql
from psycopg.conninfo import make_conninfo

from ddp import landing
from ddp.context import WriteRef
from tests.python.test_db import context


def test_landing_replay_and_failed_replacement_with_job_privileges(
    ddp_db: psycopg.Connection[object], monkeypatch: pytest.MonkeyPatch,
) -> None:
    ddp_db.execute("CREATE SCHEMA erp")
    ddp_db.execute("CREATE TABLE erp.customers (id text PRIMARY KEY, payload jsonb NOT NULL, "
                    "_loaded_at timestamptz NOT NULL, _source_key text NOT NULL)")
    role = f"ddp_pytest_job_{uuid.uuid4().hex}"
    ddp_db.execute(sql.SQL("CREATE ROLE {} NOLOGIN").format(sql.Identifier(role)))
    try:
        ddp_db.execute(sql.SQL("GRANT USAGE ON SCHEMA erp TO {}").format(sql.Identifier(role)))
        ddp_db.execute(sql.SQL("GRANT SELECT, INSERT, UPDATE, DELETE ON erp.customers TO {}")
                        .format(sql.Identifier(role)))
        monkeypatch.setenv("DATABASE_URL", make_conninfo(os.environ["DATABASE_URL"],
                                                       options=f"-c role={role}"))
        ctx = context(WriteRef("table/erp.customers", "upsert", "id"))
        for _ in range(2):
            assert landing.upsert(ctx, "erp", "customers", [{"id": "1", "name": "A"}],
                                  key="id") == 1
        assert ddp_db.execute("SELECT count(*) FROM erp.customers").fetchone() == (1,)
        assert landing.replace(ctx, "erp", "customers", [{"id": "2", "name": "B"}], key="id") == 1
        with pytest.raises(psycopg.errors.UniqueViolation):
            landing.replace(ctx, "erp", "customers", [{"id": "3"}, {"id": "3"}], key="id")
        assert ddp_db.execute("SELECT id FROM erp.customers").fetchall() == [("2",)]

        def failed_source() -> Iterator[Mapping[str, object]]:
            yield {"id": "4"}
            raise RuntimeError("source stopped")

        with pytest.raises(RuntimeError, match="source stopped"):
            landing.replace(ctx, "erp", "customers", failed_source(), key="id")
        assert ddp_db.execute("SELECT id FROM erp.customers").fetchall() == [("2",)]
        with pytest.raises(ValueError, match="non-empty"):
            landing.replace(ctx, "erp", "customers", [], key=[])
        assert ddp_db.execute("SELECT id FROM erp.customers").fetchall() == [("2",)]
        assert landing.append(ctx, "erp", "customers", [{"id": "3"}]) == 1
        with pytest.raises(psycopg.errors.UniqueViolation):
            landing.append(ctx, "erp", "customers", [{"id": "3"}])
        assert ddp_db.execute("SELECT id FROM erp.customers ORDER BY id").fetchall() == [
            ("2",), ("3",),
        ]
        assert landing.replace(ctx, "erp", "customers", [], key="id") == 0
        assert ddp_db.execute("SELECT count(*) FROM erp.customers").fetchone() == (0,)
    finally:
        ddp_db.execute(sql.SQL("DROP OWNED BY {}").format(sql.Identifier(role)))
        ddp_db.execute(sql.SQL("DROP ROLE {}").format(sql.Identifier(role)))
