"""Exercise the actual CLI in a PTY against an owned disposable database."""

import json
import os
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

import psycopg
from psycopg import sql
from terminal import Terminal

ROOT = Path(__file__).resolve().parents[2]


def main() -> None:
    stubborn = Terminal(
        [sys.executable, "-c", "import signal,time; "
         "signal.signal(signal.SIGTERM,signal.SIG_IGN); "
         "signal.signal(signal.SIGHUP,signal.SIG_IGN); "
         "print('ready',flush=True); time.sleep(60)"],
        os.environ.copy(), cwd=ROOT,
    )
    try:
        stubborn.wait("ready")
    finally:
        started = time.monotonic()
        stubborn.cleanup()
    assert time.monotonic() - started < 5, "failed PTY cleanup hung"
    base = os.environ["TEST_DATABASE_URL"]
    parts = urlsplit(base)
    if parts.scheme not in ("postgres", "postgresql"):
        raise RuntimeError("TUI fixture requires a Postgres URL")
    name = "ddp_tui_" + uuid.uuid4().hex
    database = urlunsplit(parts._replace(path="/" + name))
    with psycopg.connect(base, autocommit=True) as admin, tempfile.TemporaryDirectory() as scratch:
        admin.execute(sql.SQL("CREATE DATABASE {}").format(sql.Identifier(name)))
        try:
            binary = Path(scratch) / "ddp-cli"
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/ddp"], cwd=ROOT, check=True)
            registry = ROOT / "internal/ddp/testdata/base/ddp.yaml"
            env = {**os.environ, "DATABASE_URL": database}
            for args in (("migrate", "up"), ("jobs", "run", "ddp:cleanup")):
                subprocess.run([str(binary), "--config", str(registry), *args, "--json"],
                               env=env, check=True, capture_output=True)
            listed = subprocess.run(
                [str(binary), "--config", str(registry), "jobs", "list", "--json"],
                env=env, check=True, capture_output=True, text=True,
            )
            cleanup_row = [job["ref"] for job in json.loads(listed.stdout)["data"]].index(
                "ddp:cleanup"
            )
            with psycopg.connect(database) as conn:
                conn.execute(
                    "UPDATE ops.executions SET status='failed' WHERE job_ref='ddp:cleanup'"
                )
                conn.execute(
                    "UPDATE ops.attempts SET status='failed',stdout='synthetic terminal log'"
                )
            for role in ("ddp_readonly", "ddp_scheduler"):
                query = dict(parse_qsl(parts.query))
                query["role"] = role
                restricted = urlunsplit(parts._replace(path="/" + name, query=urlencode(query)))
                with psycopg.connect(database) as conn:
                    before = conn.execute("SELECT count(*) FROM ops.executions").fetchone()
                terminal = Terminal(
                    [str(binary), "--config", str(registry)],
                    os.environ | {"DATABASE_URL": restricted, "JOB_DATABASE_URL": ""}, cwd=ROOT,
                )
                try:
                    terminal.wait("ddp:cleanup")
                    terminal.send("5")
                    terminal.wait("Logs")
                    terminal.send("\r")
                    terminal.wait("synthetic terminal log")
                    terminal.send("\x1b")
                    terminal.wait("execution/")
                    terminal.send("3")
                    terminal.wait("ddp:cleanup")
                    terminal.send("\x1b[H" + "\x1b[B" * cleanup_row)
                    terminal.send("x")
                    terminal.wait("Run ddp:cleanup?")
                    terminal.send("RUN\r")
                    terminal.wait("refused" if role == "ddp_readonly" else "Job command completed")
                    terminal.close()
                finally:
                    terminal.cleanup()
                with psycopg.connect(database) as conn:
                    after = conn.execute("SELECT count(*) FROM ops.executions").fetchone()
                assert before is not None and after is not None
                assert after[0] == before[0] + (role == "ddp_scheduler"), (role, before, after)
            project = Path(scratch)
            (project / "ddp").symlink_to(ROOT / "ddp", target_is_directory=True)
            (project / ".venv").symlink_to(ROOT / ".venv", target_is_directory=True)
            (project / "jobs").mkdir()
            (project / "jobs/tui_wait.py").write_text(
                "import os, time\nfrom pathlib import Path\n"
                "def run(ctx):\n"
                "    Path(__file__).with_name('child.pid').write_text(str(os.getpid()))\n"
                "    time.sleep(30)\n"
            )
            waiting_registry = project / "ddp.yaml"
            waiting_registry.write_text(registry.read_text().replace(
                "jobs: {}", "jobs:\n  tui_wait:\n    purpose: Test terminal cancellation\n"
                "    action: check\n    python: jobs.tui_wait\n    timeout: 30s"
            ))
            terminal = Terminal(
                [str(binary), "--config", str(waiting_registry)],
                os.environ | {"DATABASE_URL": restricted, "JOB_DATABASE_URL": database}, cwd=ROOT,
            )
            try:
                terminal.wait("job/tui_wait")
                terminal.send("3")
                terminal.wait("Jobs")
                # The final row is this fixture's only client job.
                terminal.send("\x1b[F")
                terminal.send("x")
                terminal.wait("Run job/tui_wait?")
                terminal.send("RUN\r")
                deadline = time.monotonic() + 10
                while not (project / "jobs/child.pid").exists():
                    if time.monotonic() > deadline:
                        raise AssertionError("TUI job did not start its Python workload")
                    time.sleep(0.05)
                child = int((project / "jobs/child.pid").read_text())
                terminal.send("\x03")
                terminal.finish((0, 1))
                try:
                    os.kill(child, 0)
                except ProcessLookupError:
                    pass
                else:
                    raise AssertionError("TUI cancellation left its workload running")
                with psycopg.connect(database) as conn:
                    state = conn.execute(
                        "SELECT status FROM ops.executions WHERE job_ref='job/tui_wait'"
                    ).fetchone()
                assert state == ("interrupted",), state
            finally:
                terminal.cleanup()
            report = subprocess.run([str(binary), "--json"], cwd=ROOT, check=True,
                                    capture_output=True, text=True)
            assert json.loads(report.stdout)["ok"]
            assert "\x1b" not in report.stdout
            print("TUI PTY: logs, actions, readonly refusal, cancellation and restoration passed")
        finally:
            admin.execute(sql.SQL("DROP DATABASE {} WITH (FORCE)").format(sql.Identifier(name)))


if __name__ == "__main__":
    main()
