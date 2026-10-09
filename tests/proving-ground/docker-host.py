#!/usr/bin/env python3
"""Run the image gate against a disposable TLS Docker-in-Docker daemon."""

from __future__ import annotations

import json
import os
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from types import FrameType

DIND_IMAGE = (
    "docker:29.4.0-dind@sha256:a6dd5322747a95cd8e3207bd8d415a8fd20ec34e9c00f06dc019cbd912013489"
)
REGISTRY_IMAGE = (
    "registry:3@sha256:1be55279f18a2fe1a74edf2664cac61c1bea305b7b4642dab412e7affdcb3e33"
)


def run(
    args: list[str], *, env: dict[str, str] | None = None, check: bool = True
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, env=env, check=check, text=True)


def output(args: list[str], *, env: dict[str, str] | None = None) -> str:
    return subprocess.check_output(args, env=env, text=True).strip()


def write_registry_certs(root: Path) -> None:
    root.mkdir(parents=True)
    (root / "openssl.cnf").write_text(
        """[req]
distinguished_name=req_distinguished_name
req_extensions=v3
prompt=no
[req_distinguished_name]
CN=ghcr.io
[v3]
subjectAltName=DNS:ghcr.io
"""
    )
    run(["openssl", "genrsa", "-out", str(root / "ca.key"), "2048"])
    run(
        [
            "openssl",
            "req",
            "-x509",
            "-new",
            "-nodes",
            "-key",
            str(root / "ca.key"),
            "-sha256",
            "-days",
            "1",
            "-subj",
            "/CN=ddp proving-ground CA",
            "-out",
            str(root / "ca.crt"),
        ]
    )
    run(["openssl", "genrsa", "-out", str(root / "server.key"), "2048"])
    run(
        [
            "openssl",
            "req",
            "-new",
            "-key",
            str(root / "server.key"),
            "-out",
            str(root / "server.csr"),
            "-config",
            str(root / "openssl.cnf"),
        ]
    )
    run(
        [
            "openssl",
            "x509",
            "-req",
            "-in",
            str(root / "server.csr"),
            "-CA",
            str(root / "ca.crt"),
            "-CAkey",
            str(root / "ca.key"),
            "-CAcreateserial",
            "-out",
            str(root / "server.crt"),
            "-days",
            "1",
            "-sha256",
            "-extfile",
            str(root / "openssl.cnf"),
            "-extensions",
            "v3",
        ]
    )


def interrupted(signum: int, _frame: FrameType | None) -> None:
    raise SystemExit(128 + signum)


def main() -> int:
    signal.signal(signal.SIGTERM, interrupted)
    command = sys.argv[1:] or ["sh", "tests/proving-ground/check-image.sh"]
    outer_env = os.environ.copy()
    # Compose resolves bind sources through the host's physical working path.
    # Mount that same path in DinD, including macOS's /var -> /private/var alias.
    temp = Path(tempfile.mkdtemp(prefix="ddp-docker-host-")).resolve()
    certs = temp / "certs"
    registry_certs = temp / "registry"
    network = f"ddp-docker-host-{secrets.token_hex(6)}"
    dind = f"ddp-docker-host-{secrets.token_hex(6)}"
    registry = f"{dind}-registry"
    uid = str(os.getuid())
    gid = str(os.getgid())
    try:
        outer_docker_env = {
            key: outer_env[key]
            for key in (
                "DOCKER_HOST",
                "DOCKER_CONTEXT",
                "DOCKER_CONFIG",
                "DOCKER_TLS_VERIFY",
                "DOCKER_CERT_PATH",
                "DOCKER_TLS",
            )
            if key in outer_env
        }
        outer_env_file = temp / "outer-docker-env.json"
        outer_env_file.write_text(json.dumps(outer_docker_env) + "\n")
        outer_env_file.chmod(0o600)
        write_registry_certs(registry_certs)
        (temp / "daemon.json").write_text('{"features":{"containerd-snapshotter":true}}\n')
        run(["docker", "network", "create", "--attachable", network], env=outer_env)
        run(
            [
                "docker",
                "run",
                "-d",
                "--name",
                registry,
                "--network",
                network,
                "--network-alias",
                "ghcr.io",
                "-v",
                f"{registry_certs}:/certs:ro",
                "-e",
                "REGISTRY_HTTP_TLS_CERTIFICATE=/certs/server.crt",
                "-e",
                "REGISTRY_HTTP_TLS_KEY=/certs/server.key",
                "-e",
                "REGISTRY_HTTP_ADDR=0.0.0.0:443",
                REGISTRY_IMAGE,
            ],
            env=outer_env,
        )
        run(
            [
                "docker",
                "run",
                "-d",
                "--privileged",
                # DinD otherwise mounts over /tmp, hiding Linux fixture binds.
                "--tmpfs",
                "/tmp:rw,exec,dev",
                "--name",
                dind,
                "--network",
                network,
                "-p",
                "127.0.0.1::2376",
                "-p",
                "127.0.0.1:18081:18082",
                "-e",
                "DOCKER_TLS_CERTDIR=/certs",
                "-v",
                f"{certs}:/certs",
                "-v",
                f"{temp / 'daemon.json'}:/etc/docker/daemon.json:ro",
                "-v",
                f"{registry_certs / 'ca.crt'}:/etc/docker/certs.d/ghcr.io/ca.crt:ro",
                "-v",
                f"{registry_certs / 'ca.crt'}:"
                "/usr/local/share/ca-certificates/ddp-registry.crt:ro",
                "-v",
                f"{temp}:{temp}",
                DIND_IMAGE,
            ],
            env=outer_env,
        )
        # The entrypoint owns TLS material inside the daemon container; copy only
        # its client certificates into this private wrapper directory for the
        # outer CLI.  The child CLI continues using /certs/client in DinD.
        for _ in range(90):
            if (
                run(
                    ["docker", "exec", dind, "test", "-f", "/certs/client/ca.pem"],
                    env=outer_env,
                    check=False,
                ).returncode
                == 0
            ):
                break
            time.sleep(1)
        else:
            raise RuntimeError("DinD TLS certificates were not generated")
        client_certs = temp / "client-certs"
        run(["docker", "cp", f"{dind}:/certs/client/.", str(client_certs)], env=outer_env)
        api = output(["docker", "port", dind, "2376/tcp"], env=outer_env).rsplit(":", 1)[-1]
        host_env = outer_env | {
            "DOCKER_HOST": f"tcp://127.0.0.1:{api}",
            "DOCKER_TLS_VERIFY": "1",
            "DOCKER_CERT_PATH": str(client_certs),
        }
        host_env.pop("DOCKER_CONTEXT", None)
        for _ in range(90):
            if (
                run(
                    ["docker", "info", "--format", "{{.ServerVersion}}"], env=host_env, check=False
                ).returncode
                == 0
            ):
                break
            time.sleep(1)
        else:
            raise RuntimeError("DinD Docker API did not become ready")
        run(["docker", "exec", dind, "test", "-f", str(outer_env_file)], env=outer_env)
        run(
            [
                "docker",
                "exec",
                dind,
                "sh",
                "-eu",
                "-c",
                "apk add --no-cache bash jq util-linux socat git; update-ca-certificates; "
                "socat TCP-LISTEN:18082,fork,reuseaddr "
                "TCP:127.0.0.1:18081 >/tmp/ddp-socat.log 2>&1 &",
            ],
            env=outer_env,
        )
        run(["docker", "exec", dind, "sh", "-c", "command -v bash jq flock socat"], env=outer_env)
        # ghcr.io is deliberately occupied by the private registry. Seed the
        # public uv base image through the original outer Docker connection.
        uv_image = "ghcr.io/astral-sh/uv:0.12.9"
        uv_archive = temp / "uv-image.tar"
        for platform in ("linux/amd64", "linux/arm64"):
            run(["docker", "pull", "--platform", platform, uv_image], env=outer_env)
        run(["docker", "save", "-o", str(uv_archive), uv_image], env=outer_env)
        run(["docker", "load", "-i", str(uv_archive)], env=host_env)

        plugin_paths: list[str] = []
        info = subprocess.run(
            ["docker", "info", "--format", "{{json .ClientInfo.Plugins}}"],
            env=outer_env,
            text=True,
            capture_output=True,
            check=False,
        )
        if info.returncode == 0:
            try:
                plugin_paths = sorted(
                    {str(Path(p["Path"]).parent) for p in json.loads(info.stdout) if p.get("Path")}
                )
            except (ValueError, TypeError, KeyError):
                pass
        child_config = temp / "child-docker-config"
        child_config.mkdir(mode=0o700)
        config_file = child_config / "config.json"
        config_file.write_text(json.dumps({"cliPluginsExtraDirs": plugin_paths}) + "\n")
        config_file.chmod(0o600)
        child_env = {
            "DOCKER_HOST": f"tcp://127.0.0.1:{api}",
            "DOCKER_TLS_VERIFY": "1",
            "DOCKER_CERT_PATH": str(client_certs),
            "DOCKER_CONFIG": str(child_config),
            "DDP_TEST_DOCKER_HOST_CONTAINER": dind,
            "DDP_TEST_OUTER_DOCKER_ENV": str(outer_env_file),
            "TMPDIR": str(temp),
        }
        child_process_env = outer_env.copy()
        child_process_env.update(child_env)
        child_process_env.pop("DOCKER_CONTEXT", None)
        child = subprocess.Popen(command, env=child_process_env, start_new_session=True)
        try:
            return child.wait()
        finally:
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGTERM)
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait()
    finally:
        cleanup_env = outer_env.copy()
        run(
            [
                "docker",
                "exec",
                dind,
                "sh",
                "-c",
                'rm -rf /certs/*; chown -R "$1:$2" "$3"',
                "sh",
                uid,
                gid,
                str(temp),
            ],
            env=cleanup_env,
            check=False,
        )
        run(["docker", "rm", "-fv", dind, registry], env=cleanup_env, check=False)
        run(["docker", "network", "rm", network], env=cleanup_env, check=False)
        shutil.rmtree(temp, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
