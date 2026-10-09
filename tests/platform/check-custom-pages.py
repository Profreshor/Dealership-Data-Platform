"""Scaffold custom pages, reject build drift, and test their embedded client portal."""

import os
import shutil
import subprocess
import tempfile
from pathlib import Path

root = Path(__file__).resolve().parents[2]
fixture = root / "internal/ddp/testdata/reporting"


def assemble_client(client: Path) -> None:
    """Build a temporary source tree from the template and fixed reporting facts."""
    client.mkdir(parents=True)
    for name in (
        "go.mod", "go.sum", "pyproject.toml", "uv.lock", ".dockerignore",
        "cmd", "internal", "migrations", "frontend", "ddp", "client",
        "jobs", "deploy", "templates",
    ):
        source = root / name
        destination = client / name
        if source.is_dir():
            shutil.copytree(
                source, destination,
                ignore=shutil.ignore_patterns(
                    "node_modules", "dist", "__pycache__", "test-results", "playwright-report"
                ),
            )
        else:
            shutil.copy2(source, destination)
    for name in ("client", "jobs", "models", "migrations/app"):
        shutil.copytree(fixture / name, client / name, dirs_exist_ok=True)
    shutil.copy2(fixture / "ddp.yaml", client / "ddp.yaml")

with tempfile.TemporaryDirectory(prefix="ddp-custom-pages-") as scratch:
    scratch_path = Path(scratch)
    client = scratch_path / "client"
    assemble_client(client)
    (client / "frontend/node_modules").symlink_to(
        root / "frontend/node_modules", target_is_directory=True
    )
    registry = client / "ddp.yaml"
    registry.write_text(
        registry.read_text().replace(
            "  display_name: Unit Fixture\n",
            "  display_name: Synthetic Blue\n"
            '  branding: { logo: frontend/apps/portal/public/logo.svg, accent: "#2457c5" }\n',
        )
    )
    public = client / "frontend/apps/portal/public"
    public.mkdir(parents=True, exist_ok=True)
    logo = public / "logo.svg"
    logo.write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40">'
        '<rect width="40" height="40" rx="6" fill="#2457c5"/>'
        '<path d="M10 12h20M10 20h20M10 28h20" stroke="white" stroke-width="4"/></svg>'
    )
    binary = scratch_path / "ddp"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/ddp"], cwd=root, check=True)
    for name, label, path, access in (
        ("client_workspace", "Synthetic workspace", "/", "permission: customers.read"),
        ("custom_report", "Client report", "/custom-report", "permission: customers.read"),
        ("operator_note", "Operator note", "/operator-note", "policy: admin"),
        ("broken_report", "Retry example", "/retry-example", "permission: customers.read"),
    ):
        definition = scratch_path / "page.yaml"
        definition.write_text(f"label: {label}\npath: {path}\nkind: custom\n{access}\n")
        subprocess.run(
            [
                str(binary),
                "new",
                "page",
                name,
                "--definition",
                str(definition),
                "--config",
                str(client / "ddp.yaml"),
                "--json",
            ],
            check=True,
        )
    routes = client / "frontend/apps/portal/src/routes"
    for name in ("custom_report", "broken_report"):
        shutil.copy2(root / "tests/platform/pages" / f"{name}.tsx", routes / f"{name}.tsx")

    def build(expected_error: str = "") -> None:
        result = subprocess.run(
            ["npm", "run", "build"], cwd=client / "frontend", capture_output=True, text=True
        )
        output = result.stdout + result.stderr
        if expected_error:
            if result.returncode == 0 or expected_error not in output:
                raise RuntimeError(f"build did not reject {expected_error}:\n{output}")
        elif result.returncode != 0:
            raise RuntimeError(f"custom portal build failed:\n{output}")

    report = routes / "custom_report.tsx"
    source = report.read_text()
    report.write_text(
        source.replace('registerPage("custom_report",', 'registerPage("unknown_page",')
    )
    build("unknown_page")
    report.unlink()
    build("custom_report")
    report.write_text(source)
    duplicate = routes / "duplicate.tsx"
    duplicate.write_text(source)
    build("custom_report")
    duplicate.unlink()
    logo.rename(public / "saved.svg")
    build("logo.svg")
    (public / "saved.svg").rename(logo)
    build()
    subprocess.run(
        [
            "go",
            "test",
            "-v",
            "-count=1",
            "./internal/ddp/serving",
            "-run",
            "^TestCustomPagesBrowserAgainstPostgres$",
        ],
        cwd=client,
        env={**os.environ, "DDP_CUSTOM_BROWSER": "1"},
        check=True,
    )
    print("Custom pages and branding: scaffold, build drift and embedded browser checks passed")
