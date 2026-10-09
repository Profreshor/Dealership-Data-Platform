"""Release guards use real Git and must refuse before touching Docker."""

import os
import shutil
import subprocess
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "bin/publish-image.sh"
with tempfile.TemporaryDirectory(prefix="ddp-publish-guards-") as scratch:
    root = Path(scratch)
    checkout = root / "client"
    checkout.mkdir()
    (checkout / "bin").mkdir()
    shutil.copy2(SCRIPT, checkout / "bin/publish-image.sh")
    git = [
        "git",
        "-c",
        "core.hooksPath=/dev/null",
        "-c",
        "commit.gpgsign=false",
        "-c",
        "user.name=Release test",
        "-c",
        "user.email=release@example.test",
    ]

    def run_git(*args: str) -> str:
        return subprocess.check_output([*git, *args], cwd=checkout, text=True).strip()

    run_git("init", "-q", "-b", "main")
    run_git("add", "--all")
    run_git("commit", "-qm", "Synthetic release")
    revision = run_git("rev-parse", "HEAD")
    origin = root / "origin.git"
    run_git("init", "-q", "--bare", str(origin))
    run_git("remote", "add", "origin", str(origin))
    run_git("push", "-q", "origin", "main")
    fakebin = root / "fakebin"
    fakebin.mkdir()
    (fakebin / "docker").write_text('#!/bin/sh\ntouch "$DOCKER_LOG"\nexit 90\n')
    (fakebin / "docker").chmod(0o755)
    env = os.environ | {
        "GITHUB_EVENT_NAME": "push",
        "GITHUB_REF": "refs/heads/main",
        "GITHUB_REF_PROTECTED": "true",
        "GITHUB_REPOSITORY": "Acme/client",
        "GITHUB_SHA": revision,
        "PATH": str(fakebin) + os.pathsep + os.environ["PATH"],
        "DOCKER_LOG": str(root / "docker.log"),
    }

    def refused(changes: dict[str, str], message: str, *, succeeds: bool = False) -> None:
        result = subprocess.run(
            ["bash", "bin/publish-image.sh"],
            cwd=checkout,
            env=env | changes,
            capture_output=True,
            text=True,
        )
        assert (result.returncode == 0) == succeeds, result.stderr
        assert message in result.stderr, result.stderr
        assert not (root / "docker.log").exists(), "guard reached Docker"

    for changes, message in (
        ({"GITHUB_EVENT_NAME": "pull_request"}, "push to protected main"),
        ({"GITHUB_REF": "refs/heads/feature"}, "push to protected main"),
        ({"GITHUB_REF_PROTECTED": "false"}, "push to protected main"),
        ({"GITHUB_REPOSITORY": "acme/client:stable"}, "invalid repository"),
        ({"GITHUB_SHA": "dev"}, "invalid revision"),
        ({"GITHUB_SHA": "a" * 40}, "checkout does not match"),
    ):
        refused(changes, message)
    (checkout / "untracked").touch()
    refused({}, "checkout must be clean")
    (checkout / "untracked").unlink()
    index = checkout / ".git/index"
    original_index = index.read_bytes()
    index.write_bytes(b"corrupt")
    refused({}, "cannot inspect release checkout")
    index.write_bytes(original_index)
    run_git("commit", "--allow-empty", "-qm", "Newer main")
    run_git("push", "-q", "origin", "main")
    run_git("checkout", "-q", "--detach", revision)
    refused({}, "main has advanced", succeeds=True)
    run_git("remote", "set-url", "origin", str(root / "missing.git"))
    refused({}, "does not appear to be a git repository")
print("Publisher guards: event, protection, source revision, clean tree and stale main passed")
