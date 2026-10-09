"""Focused proving-ground checks for the host release updater."""

import fcntl
import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).parents[2] / "bin" / "update.sh"
CURRENT = "ghcr.io/acme/client@sha256:" + "b" * 64
CANDIDATE = "ghcr.io/acme/client@sha256:" + "a" * 64
REVISION = "c" * 40
FOREIGN = "ghcr.io/other/client@sha256:" + "d" * 64
JQ = shutil.which("jq")


def state(*, pending: str = "", phase: str = "preflight", previous: str = "") -> dict[str, object]:
    return {
        "version": 1,
        "current": CURRENT,
        "previous": previous,
        "failed": "",
        "pending": pending,
        "revision": REVISION if pending else "",
        "phase": phase,
    }


def run(case: str, saved: dict[str, object] | None = None, *, lock: bool = False):
    if JQ is None:
        raise RuntimeError("jq is required")
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        (root / "bin").mkdir()
        shutil.copy2(SCRIPT, root / "bin/update.sh")
        (root / ".env").write_text("POSTGRES_PASSWORD=secret\n", encoding="utf-8")
        (root / "deploy").mkdir()
        (root / "deploy/compose.yaml").write_text("services: {}\n", encoding="utf-8")
        if saved is not None:
            (root / ".ddp-release.json").write_text(json.dumps(saved) + "\n", encoding="utf-8")
        fakebin = root / "fakebin"
        fakebin.mkdir()
        docker = r'''#!/usr/bin/env python3
import json, os, sys
a = sys.argv[1:]
case = os.environ["CASE"]
with open(os.environ["DOCKER_LOG"], "a", encoding="utf-8") as log:
    log.write(" ".join(a) + " image=" + os.environ.get("DDP_IMAGE_DIGEST", "") +
              " db=" + os.environ.get("DATABASE_URL", "") + "\n")
if a[:2] == ["compose", "--env-file"] and "ps" in a:
    print("api-id")
elif a[:2] == ["inspect", "--format"]:
    print(os.environ.get("CURRENT", __CURRENT__))
elif a[:2] == ["image", "inspect"]:
    print(json.dumps([__CANDIDATE__]))
elif a[:1] == ["pull"]:
    pass
elif a[:2] == ["compose", "--env-file"] and "config" in a:
    print(json.dumps({"services": {
        "maintenance": {"environment": {"DATABASE_URL": "postgres://owner"}},
        "scheduler": {"environment": {
            "DATABASE_URL": "postgres://scheduler",
            "BACKUP_DATABASE_URL": "postgres://backup",
        }},
    }}))
elif a[:2] == ["compose", "--env-file"] and "run" in a:
    if "version" in a:
        print(json.dumps({"version": 1, "ok": True, "data": {"version": __REVISION__}}))
    elif "plan" in a:
        if case == "atomic":
            raise SystemExit(1)
        print(json.dumps({"version": 1, "ok": True, "data": {
            "action": "apply", "revision": __REVISION__, "requires_backup": False,
        }}))
    elif "models" in a and case == "rollback-model-failure":
        raise SystemExit(77)
    else:
        print('{"version":1,"ok":true,"data":{}}')
elif a[:2] == ["compose", "--env-file"]:
    pass
else:
    raise SystemExit(2)
'''.replace("__CURRENT__", repr(CURRENT)).replace("__CANDIDATE__", repr(CANDIDATE)).replace(
            "__REVISION__", repr(REVISION)
        )
        (fakebin / "docker").write_text(docker, encoding="utf-8")
        (fakebin / "docker").chmod(0o755)
        if case == "lock-error":
            (fakebin / "flock").write_text("#!/bin/sh\nexit 73\n", encoding="utf-8")
            (fakebin / "flock").chmod(0o755)
        if case == "atomic":
            (fakebin / "jq").write_text(
                "#!/bin/sh\n"
                "if [ \"$1\" = -n ]; then n=0; [ -f \"$JQ_COUNT\" ] && n=$(cat \"$JQ_COUNT\"); "
                "n=$((n+1)); echo $n >\"$JQ_COUNT\"; [ $n -ge 3 ] && exit 91; fi\n"
                f'exec {JQ!r} "$@"\n',
                encoding="utf-8",
            )
            (fakebin / "jq").chmod(0o755)
        env = os.environ | {
            "PATH": str(fakebin) + os.pathsep + os.environ["PATH"],
            "CASE": case,
            "CURRENT": CURRENT,
            "JQ_COUNT": str(root / "jq.count"),
            "DOCKER_LOG": str(root / "docker.log"),
        }
        held = None
        if lock:
            held = (root / ".ddp-update.lock").open("w")
            fcntl.flock(held, fcntl.LOCK_EX)
        process = subprocess.run(
            [str(root / "bin/update.sh")], cwd=root, env=env, text=True, capture_output=True
        )
        if held:
            held.close()
        state_file = root / ".ddp-release.json"
        actual = state_file.read_text(encoding="utf-8") if state_file.exists() else ""
        log_file = root / "docker.log"
        log = log_file.read_text(encoding="utf-8") if log_file.exists() else ""
        return process, actual, log


def main() -> None:
    # Feed the installer's actual state writer into the updater, including phase.
    installer = SCRIPT.with_name("install.sh").read_text(encoding="utf-8")
    writer = next(line for line in installer.splitlines() if line.startswith("jq -n --arg image "))
    with tempfile.TemporaryDirectory() as td:
        subprocess.run(
            ["bash", "-c", writer],
            env=os.environ | {"image": CURRENT, "revision": REVISION, "tmp": td},
            check=True,
        )
        installed = json.loads((Path(td) / "release").read_text(encoding="utf-8"))
    process, actual, _ = run("installed", installed)
    assert process.returncode == 0, process.stderr
    assert json.loads(actual)["current"] == CANDIDATE
    process, _, log = run("malformed", {"version": 1})
    assert process.returncode != 0 and log == ""
    process, _, log = run("cross-repository", state(previous=FOREIGN))
    assert process.returncode != 0 and log == ""
    process, _, log = run("lock-error")
    assert process.returncode == 73 and log == ""
    process, _, log = run("busy", lock=True)
    assert process.returncode == 0 and log == ""
    process, actual, _ = run("atomic", state())
    atomic = json.loads(actual)
    assert process.returncode != 0 and atomic["current"] == CURRENT
    assert atomic["pending"] == CANDIDATE and atomic["phase"] == "preflight"
    process, actual, log = run("recovery", state(pending=CANDIDATE, phase="models"))
    recovered = json.loads(actual)
    assert process.returncode != 0 and recovered["current"] == CURRENT
    assert recovered["failed"] == CANDIDATE and recovered["pending"] == ""
    assert "pull" not in log and "models" in log and f"image={CURRENT}" in log
    assert "db=postgres://scheduler" in log and "deploy record" in log
    process, actual, _ = run("rollback-model-failure", state(pending=CANDIDATE, phase="models"))
    recovered = json.loads(actual)
    assert process.returncode != 0 and recovered["current"] == CURRENT
    assert recovered["failed"] == CANDIDATE and recovered["pending"] == CANDIDATE
    assert recovered["phase"] == "rollback"
    print("update negative-path checks passed")


if __name__ == "__main__":
    main()
