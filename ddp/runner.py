from __future__ import annotations

import argparse
import importlib
import json
from pathlib import Path
from typing import Any

from .context import JobContext
from .result import JobResult


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run one DDP Python job")
    parser.add_argument("module")
    parser.add_argument("--context", required=True, type=Path)
    parser.add_argument("--result", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        raw = json.loads(args.context.read_text())
        context = JobContext.from_dict(raw)
        entrypoint = importlib.import_module(args.module).run
        result = entrypoint(context)
        if not isinstance(result, JobResult):
            raise TypeError("job run(ctx) must return JobResult")
        _write(args.result, result.to_dict())
        return 0
    except (Exception, SystemExit, KeyboardInterrupt) as exc:
        _write(
            args.result,
            {"error": {"type": type(exc).__name__, "message": "job failed"}},
        )
        return 1


def _write(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, allow_nan=False) + "\n")


if __name__ == "__main__":
    raise SystemExit(main())
