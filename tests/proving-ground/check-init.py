"""Initialize a real client, run its checks, and verify its embedded account seed."""

import os
import secrets
import subprocess
import tempfile
import uuid
from pathlib import Path

import psycopg
from prepare import initialize_client
from psycopg import sql
from psycopg.conninfo import make_conninfo

root = Path(__file__).resolve().parents[2]
base = os.environ.get("TEST_DATABASE_URL")
if not base:
    raise SystemExit("TEST_DATABASE_URL is required for initializer verification")

with tempfile.TemporaryDirectory(prefix="ddp-init-check-") as scratch:
    client = Path(scratch) / "client"
    with initialize_client(client):
        pass
    for name in (".venv", "frontend/node_modules"):
        (client / name).symlink_to(root / name, target_is_directory=True)
    # The generated project's checks must cover its real migrations and client
    # code. Its project.mk omits check-init, so this does not recursively initialize.
    subprocess.run(["make", "check"], cwd=client, check=True)
    client_binary = client / "build/ddp"
    incomplete = subprocess.run(
        [str(client_binary), "smoke", "--json"], cwd=client, capture_output=True, text=True
    )
    assert incomplete.returncode != 0, "fresh discovery must not claim working ingestion"
    assert "exactly one ingest job (found 0)" in incomplete.stdout, incomplete.stdout

    database = "ddp_init_" + uuid.uuid4().hex
    with psycopg.connect(base, autocommit=True) as admin:
        admin.execute(sql.SQL("CREATE DATABASE {}").format(sql.Identifier(database)))
        try:
            client_url = make_conninfo(base, dbname=database)
            env = os.environ | {"DATABASE_URL": client_url}
            subprocess.run(
                [str(client_binary), "migrate", "up", "--json"],
                cwd=client,
                env=env,
                check=True,
            )
            with psycopg.connect(client_url, autocommit=True) as conn:
                assert conn.execute(
                    "SELECT nspowner='ddp_owner'::regrole FROM pg_namespace "
                    "WHERE nspname='synthetic'"
                ).fetchone() == (True,)
                pending = conn.execute(
                    "SELECT id,email,password_hash,is_admin FROM app.users"
                ).fetchone()
                assert pending is not None and pending[1:] == (
                    "operator@example.test",
                    None,
                    False,
                ), pending
                assert conn.execute("SELECT id,name FROM app.roles").fetchall() == [
                    ("operator", "operator")
                ]
                assert conn.execute("SELECT count(*) FROM app.role_permissions").fetchone() == (0,)
                env["DDP_BOOTSTRAP_PASSWORD"] = secrets.token_urlsafe(32)
                subprocess.run(
                    [str(client_binary), "users", "bootstrap", "--email", pending[1], "--json"],
                    cwd=client,
                    env=env,
                    check=True,
                )
                assert conn.execute(
                    "SELECT id,is_admin,password_hash IS NOT NULL FROM app.users"
                ).fetchone() == (pending[0], True, True)
                assert conn.execute("SELECT user_id,role_id FROM app.user_roles").fetchall() == [
                    (pending[0], "operator")
                ]
        finally:
            admin.execute(sql.SQL("DROP DATABASE {} WITH (FORCE)").format(sql.Identifier(database)))
    print("Initialized client checks, embedded migrations, and pending bootstrap passed")
