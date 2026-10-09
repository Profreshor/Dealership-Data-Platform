"""Initialize only the disposable Compose proving ground."""

import json
import os
import shutil
import socketserver
import subprocess
import tempfile
import threading
import time
import urllib.request
from pathlib import Path
from typing import Any, ClassVar

import psycopg


def run(*args: str, database_url: str) -> None:
    subprocess.run(["ddp", *args], env={**os.environ, "DATABASE_URL": database_url}, check=True)


class SMTPFixture(socketserver.StreamRequestHandler):
    messages: ClassVar[list[bytes]] = []
    reject_next: ClassVar[bool] = False
    reject_message_id: ClassVar[str | None] = None

    def handle(self) -> None:
        self.wfile.write(b"220 synthetic SMTP\r\n")
        while line := self.rfile.readline():
            command = line.split()[0].upper()
            if command in (b"EHLO", b"HELO", b"MAIL", b"RCPT", b"NOOP"):
                self.wfile.write(b"250 OK\r\n")
            elif command == b"DATA":
                self.wfile.write(b"354 Send message\r\n")
                body = bytearray()
                while part := self.rfile.readline():
                    if part == b".\r\n":
                        break
                    body.extend(part)
                self.messages.append(bytes(body))
                response = b"550 synthetic first attempt rejected\r\n"
                selected = (
                    type(self).reject_message_id is None
                    or f"Message-ID: <{type(self).reject_message_id}@".encode() in body
                )
                reject = type(self).reject_next and selected
                if len(self.messages) > 1 and not reject:
                    response = b"250 accepted\r\n"
                if reject:
                    type(self).reject_next = False
                self.wfile.write(response)
            elif command == b"QUIT":
                self.wfile.write(b"221 Bye\r\n")
                return


def messages_for(messages: list[bytes], message_id: str) -> list[bytes]:
    """Return only SMTP captures belonging to one persisted outbox message."""
    marker = f"Message-ID: <{message_id}@".encode()
    return [body for body in messages if marker in body]


def check_comms(project: Path, registry: Path, database_url: str) -> None:
    with socketserver.TCPServer(("127.0.0.1", 0), SMTPFixture) as smtp:
        worker = threading.Thread(target=smtp.serve_forever, daemon=True)
        worker.start()
        try:
            text = registry.read_text()
            if text.count("addr: smtp:2525") != 1:
                raise RuntimeError("proving-ground communications configuration changed")
            registry.write_text(text.replace(
                "addr: smtp:2525",
                f"addr: 127.0.0.1:{smtp.server_address[1]}",
            ).replace("tls: implicit", "tls: none"))
            message = project / "message.json"
            message.write_text(json.dumps({
                "effect_key": "image/welcome", "template": "welcome",
                "recipients": ["recipient@example.test"],
                "context": {"Name": "Synthetic Operator", "LoginURL": "https://example.test/login"},
            }))
            run("comms", "test-send", "--file", str(message), "--config", str(registry),
                database_url=database_url)
            with psycopg.connect(database_url) as conn:
                saved = conn.execute(
                    "SELECT id,status,subject,text_body FROM ops.outbox WHERE effect_key=%s",
                    ("image/welcome",),
                ).fetchone()
                if saved is None or saved[1] != "pending" or "Synthetic Operator" not in saved[3]:
                    raise RuntimeError("SMTP failure did not retain the rendered message")
                conn.execute("UPDATE ops.outbox SET available_at=clock_timestamp() WHERE id=%s",
                             (saved[0],))
            (project / "templates/comms/welcome.txt").write_text("invalid template")
            relay = subprocess.Popen(
                ["ddp", "scheduler", "--config", str(registry)],
                env={**os.environ, "DATABASE_URL": database_url},
            )
            try:
                deadline = time.monotonic() + 80
                with psycopg.connect(database_url, autocommit=True) as conn:
                    while True:
                        if relay.poll() is not None:
                            raise RuntimeError("scheduler exited before email delivery")
                        completed = conn.execute(
                            "SELECT id FROM ops.executions WHERE job_ref='ddp:comms_relay' "
                            "AND dispatch='scheduler' AND status='succeeded' "
                            "AND started_at >= (SELECT created_at FROM ops.outbox WHERE id=%s)",
                            (saved[0],),
                        ).fetchone()
                        if completed is not None:
                            break
                        if time.monotonic() >= deadline:
                            raise RuntimeError("scheduler did not complete its email relay")
                        time.sleep(0.1)
            finally:
                relay.terminate()
                try:
                    relay.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    relay.kill()
                    relay.wait()
            if relay.returncode != 0:
                raise RuntimeError("email scheduler did not shut down cleanly")
            run("jobs", "show", "ddp:comms_relay", "--config", str(registry),
                "--json", database_url=database_url)
            run("inspect", "ddp:comms_relay", "--config", str(registry),
                "--json", database_url=database_url)
            with psycopg.connect(database_url) as conn:
                delivered = conn.execute(
                    "SELECT status,attempts,subject,text_body FROM ops.outbox WHERE id=%s",
                    (saved[0],),
                ).fetchone()
                if delivered != ("delivered", 2, saved[2], saved[3]):
                    raise RuntimeError("SMTP retry changed persisted content or lost its history")
            attempts = messages_for(SMTPFixture.messages, str(saved[0]))
            if len(attempts) != 2 or attempts[0] != attempts[1]:
                raise RuntimeError("SMTP retry did not send exactly the saved MIME message")
            run("comms", "show", f"message/{saved[0]}", "--json", database_url=database_url)
            check_health(project, registry, database_url)
            check_retention(registry, database_url)
            check_doctor(registry, os.environ["API_DATABASE_URL"])
        finally:
            smtp.shutdown()
            worker.join()


def check_health(project: Path, registry: Path, database_url: str) -> None:
    definition = project / "health_probe.yaml"
    source = project / "health_probe.py"
    definition.write_text(
        "purpose: Exercise final failure before a declared ingest output and recovery.\n"
        "action: ingest\ndeletions: ignore\nreads: [integration/synthetic]\n"
        "writes: [{target: table/synthetic.customers, mode: upsert, key: [id]}]\n"
        "retry: {max_attempts: 2, initial_delay: 50ms, max_delay: 50ms}\n"
    )
    source.write_text(
        "from pathlib import Path\nfrom ddp import JobContext, JobResult, job\n\n"
        "@job\ndef run(ctx: JobContext) -> JobResult:\n"
        "    if not Path(__file__).with_name('health_probe_ready').exists():\n"
        "        print('synthetic health failure', flush=True)\n"
        "        raise RuntimeError('synthetic health failure')\n"
        "    return JobResult()\n"
    )
    run("new", "job", "health_probe", "--definition", str(definition),
        "--source", str(source), "--config", str(registry), database_url=database_url)
    failed = subprocess.run(
        ["ddp", "jobs", "run", "job/health_probe", "--config", str(registry), "--json"],
        env={**os.environ, "DATABASE_URL": database_url}, capture_output=True, text=True,
    )
    if failed.returncode == 0:
        raise RuntimeError("deliberately failed health job succeeded")
    for _ in range(2):
        run("health", "evaluate", "--config", str(registry), database_url=database_url)
    with psycopg.connect(database_url) as conn:
        alerts = conn.execute(
            "SELECT message_id FROM ops.alerts WHERE rule_ref='ddp:job/job/health_probe'"
        ).fetchall()
        attempts = conn.execute(
            "SELECT count(*),count(*) FILTER (WHERE a.stdout LIKE '%synthetic health failure%') "
            "FROM ops.attempts a JOIN ops.executions e ON e.id=a.execution_id "
            "WHERE e.job_ref='job/health_probe' AND e.status='failed'"
        ).fetchone()
        if len(alerts) != 1 or alerts[0][0] is None or attempts != (2, 2):
            raise RuntimeError(
                f"final failure evidence: alerts={alerts!r}, attempts={attempts!r}, "
                f"command={failed.stdout!r}"
            )
        message_id = alerts[0][0]
    def diagnose(ref: str) -> dict[str, Any]:
        result = subprocess.run(
            ["ddp", "diagnose", ref, "--config", str(registry), "--json"],
            env={**os.environ, "DATABASE_URL": database_url},
            check=True, capture_output=True, text=True,
        )
        return json.loads(result.stdout)["data"]

    diagnosis = diagnose("job/health_probe")
    failures = diagnosis["failures"]
    if (
        diagnosis["state"] != "failing"
        or diagnosis["integration_refs"] != ["integration/synthetic"]
        or diagnosis["affected_outputs"] != ["table/synthetic.customers"]
        or len(failures) != 1
        or "synthetic health failure" not in failures[0]["last_attempt"]["stdout"]
        or failures[0]["last_attempt"]["number"] != 2
    ):
        raise RuntimeError("diagnosis lost failed execution, logs, output or integration")
    failed_ref = "execution/" + failures[0]["id"]
    if diagnose(failed_ref)["state"] != "failing":
        raise RuntimeError("explicit failed execution diagnosis was lost")
    def relay_until(expected: tuple[str, int]) -> None:
        for _ in range(10):
            run("comms", "relay", "--once", "--config", str(registry), database_url=database_url)
            with psycopg.connect(database_url) as conn:
                actual = conn.execute(
                    "SELECT status,attempts FROM ops.outbox WHERE id=%s", (message_id,)
                ).fetchone()
            if actual == expected:
                return
        raise RuntimeError(f"message did not reach {expected!r}")

    SMTPFixture.reject_next = True
    SMTPFixture.reject_message_id = message_id
    relay_until(("pending", 1))
    with psycopg.connect(database_url) as conn:
        message = conn.execute(
            "SELECT status,attempts FROM ops.outbox WHERE id=%s", (message_id,)
        ).fetchone()
        if message != ("pending", 1):
            raise RuntimeError("failed alert delivery did not remain pending")
        conn.execute("UPDATE ops.outbox SET available_at=clock_timestamp() WHERE id=%s",
                     (message_id,))
    relay_until(("delivered", 2))
    with psycopg.connect(database_url) as conn:
        message = conn.execute(
            "SELECT status,attempts FROM ops.outbox WHERE id=%s", (message_id,)
        ).fetchone()
        attempts = messages_for(SMTPFixture.messages, str(message_id))
        if message != ("delivered", 2) or len(attempts) != 2 or attempts[0] != attempts[1]:
            raise RuntimeError("alert retry lost its saved message identity or content")
    (project / "jobs/health_probe_ready").touch()
    run("jobs", "run", "job/health_probe", "--config", str(registry), database_url=database_url)
    for _ in range(2):
        run("health", "evaluate", "--config", str(registry), database_url=database_url)
    with psycopg.connect(database_url) as conn:
        kinds = conn.execute(
            "SELECT kind FROM ops.alerts WHERE rule_ref='ddp:job/job/health_probe' "
            "ORDER BY created_at"
        ).fetchall()
        if kinds != [("alert",), ("recovery",)]:
            raise RuntimeError("job recovery did not produce exactly one recovery notice")
    run("comms", "relay", "--once", "--config", str(registry), database_url=database_url)
    with psycopg.connect(database_url) as conn:
        recovery = conn.execute(
            "SELECT m.status FROM ops.alerts a JOIN ops.outbox m ON m.id=a.message_id "
            "WHERE a.rule_ref='ddp:job/job/health_probe' AND a.kind='recovery'"
        ).fetchone()
        if recovery != ("delivered",):
            raise RuntimeError("recovery notice was not delivered")
    recovered = diagnose("job/health_probe")
    if recovered["state"] != "ok" or recovered["failures"]:
        raise RuntimeError("current diagnosis did not clear the recovered job failure")
    if diagnose(failed_ref)["state"] != "failing":
        raise RuntimeError("recovery erased historical execution diagnosis")
    with psycopg.connect(database_url) as conn:
        platform_refs = conn.execute(
            "SELECT rule_ref FROM ops.alert_state WHERE rule_ref IN "
            "('ddp:database','ddp:scheduler','ddp:disk','ddp:outbox') ORDER BY rule_ref"
        ).fetchall()
        if platform_refs != [("ddp:database",), ("ddp:disk",),
                             ("ddp:outbox",), ("ddp:scheduler",)]:
            raise RuntimeError("health did not persist every implemented platform check")
        scheduler_state = conn.execute(
            "SELECT state FROM ops.alert_state WHERE rule_ref='ddp:scheduler'"
        ).fetchone()
        if scheduler_state != ("failing",):
            raise RuntimeError("stopped scheduler was incorrectly reported healthy")
    run("status", "--config", str(registry), "--json", database_url=database_url)
    run("health", "show", "ddp:job/job/health_probe", "--json", database_url=database_url)
    run("health", "alerts", "--json", database_url=database_url)


def check_doctor(registry: Path, database_url: str) -> None:
    with psycopg.connect(database_url) as conn:
        before = conn.execute(
            "SELECT (SELECT count(*) FROM ops.outbox), "
            "(SELECT count(*) FROM ops.health_evaluations), (SELECT count(*) FROM ddp.audit)"
        ).fetchone()
    messages = len(SMTPFixture.messages)
    result = subprocess.run(
        ["ddp", "doctor", "--config", str(registry), "--json"],
        env={**os.environ, "DATABASE_URL": database_url}, capture_output=True, text=True,
    )
    if result.returncode != 3:
        raise RuntimeError("doctor did not report stopped scheduler and unavailable host checks")
    report = json.loads(result.stdout)["data"]
    checks = {check["ref"]: check for check in report["checks"]}
    for ref in ("ddp:config", "ddp:database", "ddp:migrations", "ddp:clock", "ddp:smtp"):
        if checks[ref]["state"] != "ok":
            raise RuntimeError(f"doctor did not verify {ref}")
    if checks["ddp:scheduler"]["state"] != "failing":
        raise RuntimeError("doctor lost the stopped scheduler state")
    with psycopg.connect(database_url) as conn:
        after = conn.execute(
            "SELECT (SELECT count(*) FROM ops.outbox), "
            "(SELECT count(*) FROM ops.health_evaluations), (SELECT count(*) FROM ddp.audit)"
        ).fetchone()
    if before != after or len(SMTPFixture.messages) != messages:
        raise RuntimeError("doctor wrote operational state or sent mail")


def check_retention(registry: Path, database_url: str) -> None:
    with psycopg.connect(database_url) as conn:
        before = conn.execute("SELECT id,payload FROM synthetic.customers ORDER BY id").fetchall()
        saved = conn.execute(
            "UPDATE ops.outbox SET finished_at=clock_timestamp()-interval '31 days' "
            "WHERE effect_key='image/welcome' AND status='delivered' RETURNING id,payload_hash"
        ).fetchone()
        failed = conn.execute(
            "SELECT id FROM ops.executions WHERE job_ref='job/health_probe' AND status='failed'"
        ).fetchone()
        if saved is None or failed is None:
            raise RuntimeError("retention fixture is missing its delivered message or failed job")
        conn.execute(
            "UPDATE ops.attempts SET finished_at=clock_timestamp()-interval '31 days' "
            "WHERE execution_id=%s", (failed[0],)
        )
    run("jobs", "run", "ddp:cleanup", "--config", str(registry), database_url=database_url)
    with psycopg.connect(database_url) as conn:
        message = conn.execute(
            "SELECT payload_hash,content_expired_at IS NOT NULL,context='{}'::jsonb, "
            "subject IS NULL,text_body IS NULL,html_body IS NULL FROM ops.outbox WHERE id=%s",
            (saved[0],),
        ).fetchone()
        if message != (saved[1], True, True, True, True, True):
            raise RuntimeError("cleanup lost the message identity or retained expired content")
        after = conn.execute("SELECT id,payload FROM synthetic.customers ORDER BY id").fetchall()
        if before != after:
            raise RuntimeError("platform cleanup changed client rows")
    result = subprocess.run(
        ["ddp", "diagnose", f"execution/{failed[0]}", "--config", str(registry), "--json"],
        env={**os.environ, "DATABASE_URL": database_url},
        check=True, capture_output=True, text=True,
    )
    data = json.loads(result.stdout)["data"]
    attempt = data["execution"]["last_attempt"]
    if data["state"] != "failing" or not attempt["payload_expired_at"] or attempt["stdout"]:
        raise RuntimeError("expired logs lost their marker or failed execution identity")
    refused = subprocess.run(
        ["ddp", "comms", "resend", f"message/{saved[0]}", "--effect-key", "expired/resend",
         "--confirm", "--config", str(registry), "--json"],
        env={**os.environ, "DATABASE_URL": database_url}, capture_output=True, text=True,
    )
    if refused.returncode != 4:
        raise RuntimeError("resend of expired content was not refused")
    run("comms", "show", f"message/{saved[0]}", "--json", database_url=database_url)


if __name__ == "__main__":
    retry = b"Message-ID: <retry@example.test>\r\n\r\nsaved MIME\r\n"
    unrelated = b"Message-ID: <health@example.test>\r\n\r\nhealth alert\r\n"
    assert messages_for([retry, unrelated, retry], "retry") == [retry, retry]
    assert len(messages_for([retry, unrelated, retry, retry], "retry")) == 3
    database = os.environ["DATABASE_URL"]
    run("migrate", "up", database_url=database)
    with psycopg.connect(database) as conn:
        pending = conn.execute(
            "SELECT id,password_hash,is_admin FROM app.users WHERE email='operator@example.test'"
        ).fetchone()
        if pending is None or pending[1:] != (None, False):
            raise RuntimeError("initialization did not seed a pending operator")
        if conn.execute(
            "SELECT role_id FROM app.user_roles WHERE user_id=%s", (pending[0],)
        ).fetchall() != [("operator",)]:
            raise RuntimeError("initialization lost the discovered operator role")
    for component in ("owner", "api", "scheduler", "job", "backup", "readonly"):
        run("provision", component, "--json", database_url=database)
    scheduler_database = os.environ["SCHEDULER_DATABASE_URL"]
    run("models", "plan", "--json", database_url=scheduler_database)
    run("models", "apply", database_url=scheduler_database)
    scheduler = subprocess.Popen(
        ["ddp", "scheduler"], env={**os.environ, "DATABASE_URL": scheduler_database}
    )
    try:
        deadline = time.monotonic() + 45
        with psycopg.connect(scheduler_database, autocommit=True) as conn:
            while True:
                if scheduler.poll() is not None:
                    raise RuntimeError("scheduler exited before its chain completed")
                finished = conn.execute(
                    """SELECT EXISTS(
                        SELECT FROM ops.executions root JOIN ops.executions child
                        ON child.chain_id=root.id AND root.id=ANY(child.depends_on)
                        WHERE root.job_ref='job/sync_customers' AND root.dispatch='scheduler'
                        AND child.job_ref='job/refresh_customers'
                        AND root.status='succeeded' AND child.status='succeeded'
                        AND child.started_at>=root.finished_at)"""
                ).fetchone()
                if finished and finished[0]:
                    break
                if time.monotonic() >= deadline:
                    failures = conn.execute(
                        "SELECT e.job_ref,a.error FROM ops.attempts a "
                        "JOIN ops.executions e ON e.id=a.execution_id "
                        "WHERE a.status='failed' ORDER BY a.started_at DESC LIMIT 5"
                    ).fetchall()
                    raise RuntimeError(f"proving-ground ingest chain failed: {failures}")
                time.sleep(0.1)
        with urllib.request.urlopen("http://127.0.0.1:9092/metrics", timeout=6) as response:
            metrics = response.read().decode()
        for expected_metric in (
            "ddp_metrics_database_up 1", "ddp_scheduler_lag_seconds",
            "ddp_health_state", "ddp_outbox_depth", "go_goroutines",
            "process_cpu_seconds_total",
            'ddp_job_executions_total{job="job/sync_customers",status="succeeded"}',
        ):
            if expected_metric not in metrics:
                raise RuntimeError(f"scheduler metrics missing {expected_metric}")
    finally:
        scheduler.terminate()
        try:
            scheduler.wait(timeout=12)
        except subprocess.TimeoutExpired:
            scheduler.kill()
            scheduler.wait()
    if scheduler.returncode != 0:
        raise RuntimeError("proving-ground scheduler did not shut down cleanly")
    for args in (
        ("tables", "list"),
        ("tables", "show", "table/synthetic.customers"),
        ("integrations", "list"),
        ("integrations", "show", "integration/synthetic"),
        ("inspect", "model/core.customers"),
        ("inspect", "job/sync_customers"),
    ):
        result = subprocess.run(
            ["ddp", *args, "--json"],
            env={**os.environ, "DATABASE_URL": scheduler_database},
            check=True, capture_output=True, text=True,
        )
        data = json.loads(result.stdout)["data"]
        if args[:2] == ("tables", "show") and not data["exists"]:
            raise RuntimeError("source schema was omitted from inspection")
        if args[0] == "inspect" and args[1] == "model/core.customers":
            if data["model"]["relation"]["direct_dependencies"] != ["model/staging.customers"]:
                raise RuntimeError("inspection did not resolve native model dependencies")
        if args[:2] == ("integrations", "show"):
            if data["landed_tables"] != ["table/synthetic.customers"]:
                raise RuntimeError("integration inspection lost landed tables")
            if not data["relations"][0]["last_successful_job_finished_at"]:
                raise RuntimeError("integration inspection lost successful ingest history")
    with tempfile.TemporaryDirectory() as scratch:
        project = Path(scratch)
        registry = project / "ddp.yaml"
        shutil.copyfile("ddp.yaml", registry)
        for directory in ("ddp", "client", "jobs", "models", "templates"):
            shutil.copytree(directory, project / directory)
        definition = Path(scratch) / "model.yaml"
        source = Path(scratch) / "model.sql"
        definition.write_text(
            "purpose: Verify the model scaffold against seeded rows.\n"
            "materialization: view\nreads: [model/core.customers]\n"
            "contract:\n  columns:\n    id: {type: text, nullable: false}\n"
            "  unique_key: [id]\n"
        )
        source.write_text("SELECT id FROM core.customers\n")
        run(
            "new", "model", "mart.customer_ids", "--definition", str(definition),
            "--source", str(source), "--config", str(registry), database_url=scheduler_database,
        )
        run("validate", "--config", str(registry), database_url=scheduler_database)
        run("models", "apply", "--config", str(registry), database_url=scheduler_database)
        check_comms(project, registry, scheduler_database)
    with psycopg.connect(scheduler_database) as conn:
        actual = conn.execute("SELECT id FROM mart.customer_ids ORDER BY id").fetchall()
        expected = conn.execute("SELECT id FROM mart.customers ORDER BY id").fetchall()
        if not actual or actual != expected:
            raise RuntimeError("scaffolded model did not expose the seeded customer IDs")
    run("scheduler", "status", "--json", database_url=scheduler_database)
    run("jobs", "list", "--json", database_url=scheduler_database)
    run("jobs", "show", "job/sync_customers", "--json", database_url=scheduler_database)
    run("jobs", "pause", "job/sync_customers", database_url=scheduler_database)
    run("jobs", "resume", "job/sync_customers", database_url=scheduler_database)
    historical = (
        "jobs", "backfill", "job/sync_customers",
        "--from", "2025-01-01T00:00:00Z", "--through", "2025-01-01T00:00:00Z",
    )
    run(*historical, "--json", database_url=scheduler_database)
    run(*historical, "--apply", "--confirm-executions", "1", database_url=scheduler_database)
    with psycopg.connect(scheduler_database, autocommit=True) as conn:
        replay = conn.execute(
            """SELECT id FROM ops.executions WHERE job_ref='job/sync_customers'
               AND scheduled_at='2025-01-01T00:00:00Z'"""
        ).fetchone()
        if replay is None:
            raise RuntimeError("backfill did not save its historical execution")
        run("runs", "cancel", f"execution/{replay[0]}", database_url=scheduler_database)
        cancelled = conn.execute(
            "SELECT status, cancel_requested_at IS NOT NULL FROM ops.executions WHERE id=%s",
            (replay[0],),
        ).fetchone()
        if cancelled != ("interrupted", True):
            raise RuntimeError("queued backfill cancellation was not persisted")
    run("models", "verify", database_url=os.environ["SCHEDULER_DATABASE_URL"])
    run("runs", "list", "--json", database_url=os.environ["SCHEDULER_DATABASE_URL"])
    run("logs", "job/sync_customers", "--json", database_url=os.environ["SCHEDULER_DATABASE_URL"])
    run(
        "users",
        "bootstrap",
        "--email",
        "operator@example.test",
        database_url=os.environ["API_DATABASE_URL"],
    )
    with psycopg.connect(database) as conn:
        activated = conn.execute(
            "SELECT id,password_hash IS NOT NULL,is_admin "
            "FROM app.users WHERE email='operator@example.test'"
        ).fetchone()
        if activated != (pending[0], True, True):
            raise RuntimeError("bootstrap did not activate the discovered operator")
        for action, principal in (
            ("migrate.up", "postgres"),
            ("database.provision", "postgres"),
            ("models.apply", "ddp_scheduler_login"),
            ("models.refresh", "ddp_scheduler_login"),
            ("users.bootstrap", "ddp_api_login"),
        ):
            saved = conn.execute(
                "SELECT count(*), bool_and(principal=%s) FROM ddp.audit WHERE action=%s",
                (principal, action),
            ).fetchone()
            if not saved or saved[0] == 0 or not saved[1]:
                raise RuntimeError(f"missing or misattributed privileged audit for {action}")
