import json
import os
import subprocess
import sys
from dataclasses import replace
from pathlib import Path

import pytest

from ddp import JobContext, JobResult

CONTEXT: dict[str, object] = {
    "execution_id": "e1", "attempt_id": "a1", "scheduled_at": "2026-01-01T00:00:00Z",
    "timezone": "UTC", "reads": [], "writes": [], "settings": {}, "integrations": {},
    "watermarks": {},
}


def test_runner_writes_success_result(tmp_path: Path) -> None:
    context = tmp_path / "context.json"
    result = tmp_path / "result.json"
    context.write_text(json.dumps(CONTEXT))
    module = tmp_path / "sample_job.py"
    module.write_text(
        "from ddp import JobResult, job\n@job\ndef run(ctx): "
        "return JobResult(rows_written=2)\n"
    )
    completed = subprocess.run(
        [sys.executable, "-m", "ddp.runner", "sample_job", "--context", str(context),
         "--result", str(result)],
        cwd=tmp_path, env={**os.environ, "PYTHONPATH": f"{tmp_path}:{os.getcwd()}"},
        capture_output=True, text=True,
    )
    assert completed.returncode == 0
    assert json.loads(result.read_text()) == {"rows_written": 2}


def test_runner_rejects_malformed_context_and_writes_error(tmp_path: Path) -> None:
    context = tmp_path / "context.json"
    result = tmp_path / "result.json"
    context.write_text("{}")
    completed = subprocess.run(
        [sys.executable, "-m", "ddp.runner", "missing", "--context", str(context),
         "--result", str(result)],
        env=os.environ, capture_output=True, text=True,
    )
    assert completed.returncode == 1
    assert json.loads(result.read_text())["error"]["type"] == "ValueError"


def test_runner_cannot_fake_success_with_system_exit(tmp_path: Path) -> None:
    context = tmp_path / "context.json"
    result = tmp_path / "result.json"
    context.write_text(json.dumps(CONTEXT))
    module = tmp_path / "exit_job.py"
    module.write_text("from ddp import job\n@job\ndef run(ctx): raise SystemExit(0)\n")
    completed = subprocess.run(
        [sys.executable, "-m", "ddp.runner", "exit_job", "--context", str(context),
         "--result", str(result)],
        cwd=tmp_path, env={**os.environ, "PYTHONPATH": f"{tmp_path}:{os.getcwd()}"},
        capture_output=True, text=True,
    )
    assert completed.returncode == 1
    assert json.loads(result.read_text())["error"] == {
        "type": "SystemExit", "message": "job failed"
    }


def test_context_rejects_drift_and_invalid_write() -> None:
    with pytest.raises(ValueError):
        JobContext.from_dict({**CONTEXT, "extra": True})
    with pytest.raises(ValueError):
        JobContext.from_dict({**CONTEXT, "writes": [{"target": "erp.customers", "mode": "upsert"}]})


def test_effect_keys_survive_retry_but_change_for_new_executions() -> None:
    context = JobContext.from_dict(CONTEXT)
    assert context.effect_key("receipt") == replace(context, attempt_id="a2").effect_key("receipt")
    rerun = replace(context, execution_id="e2")
    assert context.effect_key("receipt") != rerun.effect_key("receipt")
    assert context.effect_key("receipt") != context.effect_key("reminder")
    with pytest.raises(ValueError, match="non-empty"):
        context.effect_key(" ")


def test_result_rejects_invalid_counts_and_non_json_watermarks() -> None:
    with pytest.raises(ValueError, match="non-negative integer"):
        JobResult(rows_written=-1)
    with pytest.raises(ValueError, match="non-negative integer"):
        JobResult(rows_read=True)
    with pytest.raises(ValueError, match="JSON values"):
        JobResult(watermark=float("nan"))
    with pytest.raises(ValueError, match="JSON values"):
        JobResult(details={"unserializable": object()})
