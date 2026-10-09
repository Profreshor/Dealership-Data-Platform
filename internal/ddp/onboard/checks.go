package onboard

const clientSmoke = `"""Run the client's declared ingest-to-page path against disposable Postgres."""

import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[1]
binary = root / "build/ddp"
binary.parent.mkdir(exist_ok=True)
subprocess.run(["go", "build", "-o", str(binary), "./cmd/ddp"], cwd=root, check=True)
subprocess.run([str(binary), "smoke", "--json"], cwd=root, check=True)
`

const clientImage = `#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
image="ddp-client-check-$$:local"
cleanup() {
  result=$?
  docker image rm "$image" >/dev/null 2>&1 || true
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker build -f deploy/Dockerfile -t "$image" .
docker run --rm --entrypoint sh "$image" -c '
  test "$(id -u)" = 10001
  test ! -e /src
  test ! -e /opt/ddp/tests
  test ! -e /opt/ddp/.env
  ddp validate --json
  python --version
  pg_dump --version
'
`
