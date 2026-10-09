"""Run the actual smoke CLI from a separate synthetic source checkout."""

import json
import os
import secrets
import subprocess
import sys
import tempfile
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path

from server import Handler

root = Path(__file__).resolve().parents[2]
fixture = root / "tests/proving-ground"
os.environ["SYNTHETIC_API_KEY"] = secrets.token_urlsafe(24)

with tempfile.TemporaryDirectory(prefix="ddp-smoke-check-") as scratch:
    client = Path(scratch) / "client"
    subprocess.run([sys.executable, str(fixture / "prepare.py"), str(client)], check=True)
    for source, target in (
        (root / ".venv", client / ".venv"),
        (root / "frontend/node_modules", client / "frontend/node_modules"),
    ):
        target.symlink_to(source, target_is_directory=True)
    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        registry = (client / "ddp.yaml").read_text().replace(
            "http://synthetic:18080", f"http://127.0.0.1:{server.server_port}"
        )
        (client / "ddp.yaml").write_text(registry)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            subprocess.run(["npm", "run", "build", "--prefix", "frontend"], cwd=client, check=True)
            binary = client / "ddp-cli"
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/ddp"], cwd=client, check=True)
            subprocess.run([str(binary), "validate", "--json"], cwd=client, check=True)
            result = subprocess.run(
                [str(binary), "smoke", "--json"],
                cwd=client,
                check=True,
                stdout=subprocess.PIPE,
                text=True,
            )
            report = json.loads(result.stdout)
            assert report["ok"] and report["data"]["status"] == "passed", report
            print(result.stdout, end="")
        finally:
            server.shutdown()
            worker.join()
