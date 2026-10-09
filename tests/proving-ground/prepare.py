"""Initialize a synthetic client, then add its tested ingest and reporting code."""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from collections.abc import Generator
from contextlib import contextmanager
from datetime import datetime, timedelta
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = ROOT / "tests/proving-ground"


@contextmanager
def initialize_client(target: Path) -> Generator[Path, None, None]:
    """Run the actual initializer against a committed snapshot of the candidate."""
    with tempfile.TemporaryDirectory(prefix="ddp-template-check-") as scratch:
        template = Path(scratch) / "template"
        template.mkdir()
        inventory = subprocess.check_output(["git", "ls-files", "--cached", "-z"], cwd=ROOT)
        for name in inventory.decode().split("\0"):
            if name:
                destination = template / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(ROOT / name, destination, follow_symlinks=False)
        git_env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        for args in (
            ["init", "-q"],
            ["add", "--all"],
            ["commit", "-qm", "Synthetic template snapshot"],
        ):
            subprocess.run(
                [
                    "git",
                    "-c",
                    "core.hooksPath=/dev/null",
                    "-c",
                    "commit.gpgsign=false",
                    "-c",
                    "user.name=Initializer test",
                    "-c",
                    "user.email=init@example.test",
                    *args,
                ],
                cwd=template,
                env=git_env,
                check=True,
            )
        (template / "frontend/node_modules").symlink_to(ROOT / "frontend/node_modules")
        subprocess.run(["npm", "run", "build", "--prefix", "frontend"], cwd=template, check=True)
        binary = Path(scratch) / "ddp"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/ddp"], cwd=template, check=True)
        command = [
            str(binary),
            "init",
            str(target),
            "--template",
            str(template),
            "--discovery",
            str(FIXTURE / "discovery.yaml"),
            "--json",
        ]
        env = os.environ | {"DATABASE_URL": "invalid", "TEST_DATABASE_URL": "invalid"}
        preview = subprocess.run(
            [*command, "--dry-run"], env=env, check=True, capture_output=True, text=True
        )
        assert json.loads(preview.stdout)["data"]["dry_run"] and not target.exists()
        subprocess.run(command, env=env, check=True, stdout=subprocess.DEVNULL)
        assert (target / "AGENTS.md").read_bytes() == (ROOT / "AGENTS.md").read_bytes()
        assert (target / "CLAUDE.md").read_bytes() == (ROOT / "CLAUDE.md").read_bytes()
        for vendor in (".agents", ".claude"):
            link = target / vendor / "skills"
            assert link.is_symlink() and link.readlink() == Path("../skills")
            for skill in (ROOT / "skills").glob("*/SKILL.md"):
                installed = link / skill.relative_to(ROOT / "skills")
                assert installed.read_bytes() == skill.read_bytes()
        assert not (target / "tests/proving-ground").exists()
        assert not (target / ".env").exists()
        yield binary


def section(registry: str, name: str) -> str:
    # ponytail: splice only these controlled fixture sections; use Go YAML nodes
    # if arbitrary registry editing is ever needed here.
    matches = list(re.finditer(rf"(?ms)^{re.escape(name)}:.*?(?=^[a-z][a-z_]*:|\Z)", registry))
    if len(matches) != 1:
        raise ValueError(f"expected one fixture section: {name}")
    return matches[0].group()


def prepare(target: Path) -> None:
    with initialize_client(target) as binary:
        for name in ("client", "jobs", "models"):
            shutil.copytree(FIXTURE / name, target / name, dirs_exist_ok=True)
        for name in ("compose.yaml", "server.py", "setup.py"):
            shutil.copy2(FIXTURE / name, target / name)
        registry_path = target / "ddp.yaml"
        generated = registry_path.read_text()
        registry = generated
        implementation = (FIXTURE / "implementation.yaml").read_text()
        for name in (
            "scheduler", "jobs", "tables", "models", "health", "endpoints", "pages", "permissions"
        ):
            registry = registry.replace(section(registry, name), section(implementation, name), 1)
        if registry.count("  synthetic:\n") != 1:
            raise ValueError("discovery must declare the synthetic integration")
        registry = registry.replace(
            "  synthetic:\n",
            "  synthetic:\n    base_url: http://synthetic:18080\n"
            "    auth: {type: api_key, header: X-API-Key, secret: SYNTHETIC_API_KEY}\n",
            1,
        )
        registry = registry.replace("https://synthetic.example.test", "http://localhost:18081", 1)
        for name in ("ddp", "database", "comms", "deploy"):
            assert section(registry, name) == section(generated, name), name
        registry = registry.replace(
            "comms:\n",
            "comms:\n  smtp:\n    addr: smtp:2525\n"
            "    from: sender@example.test\n    tls: implicit\n",
            1,
        )
        registry_path.write_text(registry)
        seed = list((target / "migrations/app").glob("*_discovery.sql"))
        assert len(seed) == 1, "initialization must supply the discovery migration"
        seed_bytes = seed[0].read_bytes()
        latest = max(path.name[:14] for path in (target / "migrations/app").glob("*.sql"))
        timestamp = datetime.strptime(latest, "%Y%m%d%H%M%S") + timedelta(seconds=1)
        subprocess.run(
            [
                str(binary),
                "new",
                "migration",
                timestamp.strftime("%Y%m%d%H%M%S") + "_customers",
                "--source",
                str(FIXTURE / "customers.sql"),
                "--config",
                str(registry_path),
                "--json",
            ],
            check=True,
        )
        assert seed[0].read_bytes() == seed_bytes, "onboarding changed the discovery migration"
        subprocess.run([str(binary), "validate", "--config", str(registry_path)], check=True)


if __name__ == "__main__":
    prepare(Path(sys.argv[1]).absolute())
