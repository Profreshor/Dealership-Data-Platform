"""Observe freshness and one real failed ingest across the installed operator surfaces."""

import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "tests/platform"))
from terminal import Terminal  # noqa: E402


def main() -> None:
    scratch, project = Path(sys.argv[1]), sys.argv[2]
    assert project.startswith("ddp-image-"), "only the disposable image fixture is supported"
    client = scratch / "client"
    values = dict(line.split("=", 1) for line in (scratch / "host.env").read_text().splitlines())
    command = [
        "docker",
        "compose",
        "-p",
        project,
        "--env-file",
        str(scratch / "host.env"),
        "-f",
        str(client / "deploy/compose.yaml"),
        "-f",
        str(client / "compose.yaml"),
    ]

    def compose(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
        result = subprocess.run([*command, *args], capture_output=True, text=True)
        if check and result.returncode:
            raise AssertionError(result.stdout + result.stderr)
        return result

    def ddp(*args: str) -> dict[str, Any]:
        result = compose("run", "--rm", "--no-deps", "scheduler", *args, "--json")
        report = json.loads(result.stdout)
        assert report["ok"], report
        return report["data"]

    def sql(query: str) -> str:
        return compose(
            "exec",
            "-T",
            "postgres",
            "psql",
            "-U",
            "postgres",
            "-d",
            "ddp",
            "-At",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            query,
        ).stdout.strip()

    job = "job/sync_customers"
    rule = "health/customers_fresh"
    ddp("jobs", "pause", job)
    # Drain active work so each observation has one unambiguous execution history.
    compose("stop", "scheduler")
    assert sql("SELECT paused FROM ops.job_overrides WHERE job_ref='job/sync_customers'") == "t"
    refused = compose(
        "run", "--rm", "--no-deps", "scheduler", "jobs", "run", job, "--json", check=False
    )
    assert refused.returncode == 4, refused.stdout
    ddp("health", "evaluate")
    assert ddp("health", "show", rule)["state"] == "ok"
    before = int(sql("SELECT count(*) FROM ops.alerts WHERE rule_ref='health/customers_fresh'"))
    # Advance only synthetic observation timestamps, without a five-minute wall-clock wait.
    sql("UPDATE synthetic.customers SET _loaded_at=clock_timestamp()-interval '6 minutes'")
    for _ in range(2):
        ddp("health", "evaluate")
    assert ddp("health", "show", rule)["state"] == "failing"
    assert (
        int(sql("SELECT count(*) FROM ops.alerts WHERE rule_ref='health/customers_fresh'"))
        == before + 1
    )
    assert (
        sql(
            "SELECT kind FROM ops.alerts WHERE rule_ref='health/customers_fresh' "
            "ORDER BY created_at DESC,id DESC LIMIT 1"
        )
        == "alert"
    )
    ddp("jobs", "resume", job)
    ddp("jobs", "run", job)
    for _ in range(2):
        ddp("health", "evaluate")
    assert ddp("health", "show", rule)["state"] == "ok"
    assert (
        int(sql("SELECT count(*) FROM ops.alerts WHERE rule_ref='health/customers_fresh'"))
        == before + 2
    )
    assert (
        sql(
            "SELECT kind FROM ops.alerts WHERE rule_ref='health/customers_fresh' "
            "ORDER BY created_at DESC,id DESC LIMIT 1"
        )
        == "recovery"
    )
    # The SMTP fixture rejects its first message; advance only synthetic retry times.
    pending = int(sql("SELECT count(*) FROM ops.outbox WHERE status='pending'"))
    for _ in range(pending + 1):
        sql("UPDATE ops.outbox SET available_at=clock_timestamp() WHERE status='pending'")
        ddp("comms", "relay", "--once")
    assert (
        sql(
            "SELECT count(*) FROM (SELECT message_id FROM ops.alerts "
            "WHERE rule_ref='health/customers_fresh' ORDER BY created_at DESC,id DESC LIMIT 2) a "
            "JOIN ops.outbox m ON m.id=a.message_id "
            "WHERE m.status='delivered' AND m.subject IS NOT NULL AND m.text_body IS NOT NULL"
        )
        == "2"
    ), sql(
        "SELECT a.kind,m.status,m.last_error FROM ops.alerts a "
        "LEFT JOIN ops.outbox m ON m.id=a.message_id "
        "WHERE a.rule_ref='health/customers_fresh' ORDER BY a.created_at DESC LIMIT 2"
    )

    # Stop the source to exercise the normal ingestion failure path.
    compose("stop", "synthetic")
    failed = compose(
        "run", "--rm", "--no-deps", "scheduler", "jobs", "run", job, "--json", check=False
    )
    assert failed.returncode == 1, failed.stdout
    diagnosis = ddp("diagnose", job)
    assert diagnosis["state"] == "failing", diagnosis
    assert diagnosis["integration_refs"] == ["integration/synthetic"]
    assert diagnosis["affected_outputs"] == ["table/synthetic.customers"]
    assert len(diagnosis["failures"]) == 1, diagnosis
    execution = diagnosis["failures"][0]
    run_id = execution["id"]
    assert execution["last_attempt"]["number"] == 2
    assert execution["last_attempt"]["error"]
    assert ddp("status")["recent_failures"][0]["id"] == run_id
    assert ddp("diagnose", f"execution/{run_id}")["state"] == "failing"
    evidence = scratch / "operations.json"
    evidence.write_text(json.dumps({"execution": execution, "diagnosis": diagnosis}))

    readonly = f"postgres://ddp_readonly_login:{values['READONLY_DATABASE_PASSWORD']}@postgres:5432/ddp?sslmode=disable"
    terminal = Terminal(
        [*command, "exec", "-it", "-e", "DATABASE_URL", "api", "ddp"],
        os.environ | {"DATABASE_URL": readonly},
        cwd=ROOT,
        rows=60,
    )
    try:
        terminal.wait("DDP  /  Overview")
        terminal.send("2")
        terminal.wait("[2 Failures]", f"execution/{run_id}")
        terminal.send("\x1b[H")
        terminal.send("d")
        terminal.wait(
            f'"ref": "execution/{run_id}"',
            '"state": "failing"',
            "integration/synthetic",
            "table/synthetic.customers",
        )
        terminal.send("\x1b")
        terminal.wait(f"> execution/{run_id}")
        terminal.send("l")
        terminal.wait(
            "logs", "attempt 2", execution["last_attempt"]["error"][:60]
        )
        terminal.close()
    finally:
        terminal.cleanup()
    subprocess.run(
        ["node", "--input-type=module"],
        input=(ROOT / "tests/proving-ground/browser.mjs").read_text(),
        text=True,
        cwd=ROOT / "frontend",
        env=os.environ
        | {"DDP_BASE_URL": "http://localhost:18081", "DDP_OPERATION_EVIDENCE": str(evidence)},
        check=True,
    )
    compose("up", "-d", "--wait", "synthetic")
    ddp("jobs", "run", job)
    ddp("models", "apply")
    ddp("health", "evaluate")
    assert ddp("diagnose", job)["state"] != "failing"
    assert ddp("diagnose", f"execution/{run_id}")["state"] == "failing"
    compose("up", "-d", "--no-deps", "--wait", "scheduler")
    print(
        "Proving-ground freshness and shared CLI/TUI/console failure checks passed"
    )


if __name__ == "__main__":
    main()
