"""Inspect the running production topology with synthetic overrides."""

import json
import subprocess
import sys
from pathlib import Path
from typing import Any

model: dict[str, Any] = json.loads(Path(sys.argv[1]).read_text())
services = model["services"]
assert "ports" not in services["postgres"]
assert "@sha256:" in services["postgres"]["image"]
assert "@sha256:" in services["cloudflared"]["image"]
assert services["cloudflared"]["network_mode"] == "host"
assert services["cloudflared"]["environment"]["TUNNEL_TOKEN"] == "synthetic-token-never-used"
assert "synthetic-token-never-used" not in services["cloudflared"]["command"]
assert services["maintenance"]["profiles"] == ["maintenance"]
assert services["scheduler"]["depends_on"]["api"]["condition"] == "service_healthy"
assert services["api"]["ports"][0]["host_ip"] == "127.0.0.1"

for service in ("api", "scheduler", "postgres"):
    container = subprocess.check_output(
        ["docker", "ps", "-q", "--filter", f"label=com.docker.compose.project={model['name']}",
         "--filter", f"label=com.docker.compose.service={service}"], text=True,
    ).strip()
    assert container and "\n" not in container, f"missing or duplicate {service}"
    runtime = json.loads(subprocess.check_output(["docker", "inspect", container], text=True))[0]
    if service == "postgres":
        assert not runtime["HostConfig"]["PortBindings"]
        continue
    assert runtime["HostConfig"]["ReadonlyRootfs"]
    assert runtime["HostConfig"]["CapDrop"] == ["ALL"]
    assert "no-new-privileges:true" in runtime["HostConfig"]["SecurityOpt"]
    assert runtime["Config"]["User"] == "10001:10001"
    env = dict(value.split("=", 1) for value in runtime["Config"]["Env"])
    for forbidden in ("OWNER_DATABASE_URL", "OWNER_DATABASE_PASSWORD", "POSTGRES_PASSWORD",
                      "BACKUP_AGE_IDENTITY", "TUNNEL_TOKEN", "READONLY_DATABASE_PASSWORD"):
        assert forbidden not in env, f"maintenance secret reached {service}: {forbidden}"
    assert f"ddp_{service}_login:" in env["DATABASE_URL"]
    if service == "api":
        assert not any(key.startswith(("BACKUP_", "JOB_", "SYNTHETIC_")) for key in env)
    else:
        assert "ddp_job_login:" in env["JOB_DATABASE_URL"]
        assert "ddp_backup_login:" in env["BACKUP_DATABASE_URL"]
        assert env["SYNTHETIC_API_KEY"] == "synthetic-api-only"
        subprocess.run(
            ["docker", "exec", container, "python", "-c",
             "import os,tempfile,urllib.request; "
             "assert os.getuid()==10001; "
             "f=tempfile.TemporaryFile(dir=os.environ['TMPDIR']); f.write(b'probe'); f.close(); "
             "urllib.request.urlopen('http://127.0.0.1:9092/metrics',timeout=6).close()"],
            check=True,
        )

print("Production Compose: logins, private database, secret isolation and writable staging passed")
