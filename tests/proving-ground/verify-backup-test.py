#!/usr/bin/env python3
"""Small proving-ground checks for the host restore wrapper."""

# ruff: noqa: E501
import fcntl
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).parents[2] / "bin" / "verify-backup.sh"
GOOD = "ghcr.io/acme/client@sha256:" + "a" * 64
CURRENT = "ghcr.io/acme/client@sha256:" + "b" * 64
REVISION = "c" * 40
PASSWORD = "p@ss word?&"


def run(case: str, *, lock: bool = False) -> tuple[subprocess.CompletedProcess[str], str]:
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        (root / "bin").mkdir()
        shutil.copy2(SCRIPT, root / "bin/verify-backup.sh")
        (root / ".env").write_text("POSTGRES_PASSWORD=secret\n", encoding="utf-8")
        (root / "deploy").mkdir()
        (root / "deploy/compose.yaml").write_text("services: {}\n", encoding="utf-8")
        fakebin = root / "fakebin"
        fakebin.mkdir()
        stub = """#!/usr/bin/env python3
import os, sys
a=sys.argv[1:]
case=os.environ['CASE']
if a[:3] == ['compose', '--env-file', '.env'] and 'ps' in a: print('api-id'); raise SystemExit
if a[:2] == ['inspect', '--format']: print(os.environ.get('CURRENT', __CURRENT__)); raise SystemExit
if a[:2] == ['image', 'inspect']: raise SystemExit(0 if case != 'pull-failure' else 1)
if a[:1] == ['pull']: raise SystemExit(1)
if a[:3] == ['compose', '--env-file', '.env'] and 'config' in a:
 print('{}' if case == 'missing-password' else __CONFIG__); raise SystemExit
if a[:3] == ['compose', '--env-file', '.env'] and 'run' in a:
 open(os.environ['LOG'], 'a').write(' '.join(a) + ' envdb=' + ('yes' if os.environ.get('DATABASE_URL') else 'no') + '\\n')
 if 'list' in a:
  print(os.environ.get('LIST', __DEFAULT_LIST__)); raise SystemExit
 if 'version' in a:
  print('{"version":1,"ok":true,"data":{"version":"' + os.environ.get('VERSION', __REVISION__) + '"}}'); raise SystemExit
 if 'restore' in a:
  assert os.environ['DATABASE_URL'] == 'postgres://postgres:p%40ss%20word%3F%26@postgres:5432/ddp?sslmode=disable'
  assert os.environ['DDP_IMAGE_DIGEST'] == __GOOD__
  assert '--verify' in a and '--if-due' in a
  raise SystemExit(1 if case == 'restore-failure' else 0)
raise SystemExit(0)
"""
        stub = stub.replace("__GOOD__", repr(GOOD)).replace("__CURRENT__", repr(CURRENT)).replace(
            "__CONFIG__",
            repr(
                '{"services":{"postgres":{"environment":{"POSTGRES_PASSWORD":"' + PASSWORD + '"}}}}'
            ),
            1,
        )
        stub = stub.replace("__REVISION__", repr(REVISION)).replace(
            "__DEFAULT_LIST__",
            repr(
                '{"version":1,"ok":true,"data":[{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","image":"'
                + GOOD
                + '","revision":"'
                + REVISION
                + '"}]}'
            ),
        )
        (fakebin / "docker").write_text(stub, encoding="utf-8")
        (fakebin / "docker").chmod(0o755)
        if case == "lock-error":
            (fakebin / "flock").write_text("#!/bin/sh\nexit 73\n")
            (fakebin / "flock").chmod(0o755)
        current = "ghcr.io/acme/client:stable" if case == "mutable-current" else CURRENT
        log = root / "docker.log"
        env = os.environ | {
            "PATH": str(fakebin) + os.pathsep + os.environ["PATH"],
            "CASE": case,
            "CURRENT": current,
            "LOG": str(log),
        }
        if case == "foreign":
            env["LIST"] = (
                '{"version":1,"ok":true,"data":[{"id":"x","image":"ghcr.io/other/client@sha256:'
                + "a" * 64
                + '","revision":"'
                + REVISION
                + '"}]}'
            )
        elif case == "empty":
            env["LIST"] = '{"version":1,"ok":true,"data":[]}'
        elif case == "version-mismatch":
            env["VERSION"] = "d" * 40
        if lock:
            lockfile = root / ".ddp-backup-verify.lock"
            lockfile.touch()
            held = lockfile.open("w")
            fcntl.flock(held, fcntl.LOCK_EX)
        else:
            held = None
        p = subprocess.run(
            [str(root / "bin/verify-backup.sh")], cwd=root, env=env, text=True, capture_output=True
        )
        assert (root / ".env").read_text() == "POSTGRES_PASSWORD=secret\n"
        if held:
            held.close()
        return p, log.read_text(encoding="utf-8") if log.exists() else ""


def main() -> None:
    assert run("foreign")[0].returncode != 0
    assert run("empty")[0].returncode != 0
    assert run("version-mismatch")[0].returncode != 0
    assert run("pull-failure")[0].returncode != 0
    assert run("restore-failure")[0].returncode != 0
    assert run("missing-password")[0].returncode != 0
    assert run("lock-error")[0].returncode == 73
    p, log = run("success")
    assert p.returncode == 0
    assert "envdb=yes" in log
    assert PASSWORD not in log and PASSWORD not in p.stderr and PASSWORD not in p.stdout
    assert "DATABASE_URL=" not in log
    p, log = run("mutable-current")
    assert p.returncode != 0
    p, log = run("busy", lock=True)
    assert p.returncode == 0 and log == ""
    print("verify-backup negative-path checks passed")


if __name__ == "__main__":
    main()
