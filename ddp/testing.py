"""Optional pytest fixtures; load with pytest_plugins = [\"ddp.testing\"]."""

from __future__ import annotations

import os
import uuid
from collections.abc import Generator

import psycopg
import pytest
from psycopg import sql
from psycopg.conninfo import make_conninfo


@pytest.fixture
def ddp_db(monkeypatch: pytest.MonkeyPatch) -> Generator[psycopg.Connection[object], None, None]:
    """Create an isolated schema set in a fresh database; never use DATABASE_URL."""
    url = os.environ.get("TEST_DATABASE_URL")
    if not url:
        pytest.skip("TEST_DATABASE_URL is not set")
    name = f"ddp_pytest_{uuid.uuid4().hex}"
    with psycopg.connect(url, autocommit=True) as admin:
        admin.execute(sql.SQL("CREATE DATABASE {} TEMPLATE template0").format(sql.Identifier(name)))
        try:
            test_url = make_conninfo(url, dbname=name)
            monkeypatch.setenv("DATABASE_URL", test_url)
            with psycopg.connect(test_url, autocommit=True) as connection:
                for schema in ("app", "staging", "core", "mart"):
                    connection.execute(sql.SQL("CREATE SCHEMA {}").format(sql.Identifier(schema)))
                yield connection
        finally:
            admin.execute(sql.SQL("DROP DATABASE {} WITH (FORCE)").format(sql.Identifier(name)))
