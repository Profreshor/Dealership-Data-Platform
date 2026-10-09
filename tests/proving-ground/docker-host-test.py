"""Cancellation removes the disposable Docker host and its child process."""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

wrapper = Path(__file__).with_name("docker-host.py")
with tempfile.TemporaryDirectory(prefix="ddp-host-cancel-") as scratch:
    marker = Path(scratch) / "host.json"
    child = """
import json, os, signal, sys, time
from pathlib import Path
Path(sys.argv[1]).write_text(json.dumps({
    "host": os.environ["DDP_TEST_DOCKER_HOST_CONTAINER"],
    "scratch": os.environ["TMPDIR"], "pid": os.getpid(),
}))
os.kill(os.getppid(), signal.SIGTERM)
time.sleep(60)
"""
    result = subprocess.run(
        [sys.executable, str(wrapper), sys.executable, "-c", child, str(marker)]
    )
    assert marker.is_file(), "Docker host did not reach its child command"
    state = json.loads(marker.read_text())
    host = state["host"]
    try:
        assert result.returncode == 143, result.returncode
        assert not Path(state["scratch"]).exists(), "Docker host staging remained"
        try:
            os.kill(state["pid"], 0)
        except ProcessLookupError:
            pass
        else:
            raise AssertionError("host command survived cancellation")
        for name in (host, host + "-registry"):
            probe = subprocess.run(
                ["docker", "container", "inspect", name], capture_output=True, text=True
            )
            assert probe.returncode != 0 and "No such container" in probe.stderr, probe.stderr
    finally:
        # Keep a failed regression check from leaving its own privileged daemon.
        subprocess.run(
            ["docker", "rm", "-fv", host, host + "-registry"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
print("Docker host cancellation: child, daemon and private staging cleaned")
