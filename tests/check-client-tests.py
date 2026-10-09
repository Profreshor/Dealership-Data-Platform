"""Prove platform checks in an isolated copy with fixed platform boundaries.

Generated client migrations, app composition, and portal routes are replaced
with the empty platform fixture before tests run; ``--smoke`` runs the same
copy through the platform browser and custom-page checks.
"""

import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

root = Path(__file__).resolve().parents[1]
smoke = sys.argv[1:] == ["--smoke"]
if sys.argv[1:] not in ([], ["--smoke"]):
    raise SystemExit("usage: check-client-tests.py [--smoke]")
if not os.environ.get("TEST_DATABASE_URL"):
    raise SystemExit("TEST_DATABASE_URL is required for client fixture verification")

with tempfile.TemporaryDirectory(prefix="ddp-client-tests-") as scratch:
    client = Path(scratch)
    for name in (
        "cmd",
        "internal",
        "migrations",
        "frontend",
        "ddp",
        "client",
        "jobs",
        "templates",
        "schema",
        "tests/platform",
        "deploy",
    ):
        shutil.copytree(
            root / name,
            client / name,
            ignore=shutil.ignore_patterns(
                "node_modules", "dist", "__pycache__", "test-results", "playwright-report"
            ),
        )
    for name in ("go.mod", "go.sum", "pyproject.toml", "uv.lock", ".dockerignore"):
        shutil.copyfile(root / name, client / name)
    for name in (".venv", "frontend/node_modules"):
        (client / name).symlink_to(root / name, target_is_directory=True)

    # Platform checks must not execute generated client migrations or client
    # route composition. Keep this copy's boundaries fixed and empty.
    shutil.rmtree(client / "migrations/app")
    shutil.copytree(root / "internal/ddp/testdata/base/migrations/app", client / "migrations/app")
    shutil.rmtree(client / "internal/app")
    shutil.copytree(root / "internal/ddp/testdata/base/internal/app", client / "internal/app")
    if (client / "frontend/apps/portal/src/routes").exists():
        shutil.rmtree(client / "frontend/apps/portal/src/routes")
    (client / "frontend/apps/portal/src/routes").mkdir(parents=True)

    registry = (root / "internal/ddp/testdata/base/ddp.yaml").read_text()
    for before, after in (
        ("  name: ddp\n", "  name: client_fixture\n"),
        ("  display_name: DDP\n", "  display_name: Configured Client\n"),
        ("  timezone: America/Chicago\n", "  timezone: UTC\n"),
        ("  max_workers: 4\n", "  max_workers: 1\n"),
        ("integrations: {}\n", "integrations:\n  client_http: { kind: http }\n"),
        ("permissions: {}\n", "permissions:\n  client.read: { description: Read client data. }\n"),
        ("    label: System\n", "    label: Operations\n"),
    ):
        if registry.count(before) != 1:
            raise ValueError(f"base fixture changed: {before!r}")
        registry = registry.replace(before, after)
    (client / "ddp.yaml").write_text(registry)
    assert not (client / "tests/proving-ground").exists()
    subprocess.run(["npm", "run", "build", "--prefix", "frontend"], cwd=client, check=True)
    subprocess.run(["go", "run", "./cmd/ddp", "validate", "--json"], cwd=client, check=True)
    if not smoke:
        subprocess.run(["go", "test", "-race", "-count=1", "./..."], cwd=client, check=True)
    if smoke:
        subprocess.run(
            ["uv", "run", "--locked", "python", "tests/platform/check-tui.py"],
            cwd=client,
            check=True,
        )
        for pattern in (
            "Test(Account|Administration)BrowserAgainstPostgres",
            "TestSystemBrowserAgainstPostgres",
            "TestTableBrowserAgainstPostgres",
        ):
            env = os.environ | {
                "DDP_ACCOUNT_BROWSER": "1",
                "DDP_SYSTEM_BROWSER": "1",
                "DDP_TABLE_BROWSER": "1",
            }
            subprocess.run(
                ["go", "test", "-count=1", "./internal/ddp/serving", "-run", pattern],
                cwd=client,
                env=env,
                check=True,
            )
        subprocess.run(["python3", "tests/platform/check-custom-pages.py"], cwd=client, check=True)
    print("Client configuration and proving-ground isolation: platform tests passed")
