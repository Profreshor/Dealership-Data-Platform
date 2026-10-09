#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
scratch=$(mktemp -d "${TMPDIR:-/tmp}/ddp-image.XXXXXX")
project="ddp-image-$$"
archive_image="ghcr.io/acme-example/proving-ground:$project-archive"
forward_image="ghcr.io/acme-example/proving-ground:$project-forward"
export DDP_TEST_IMAGE="$project:fixture"
compose="$scratch/client/deploy/compose.yaml"
override="$scratch/client/compose.yaml"
compose() {
  docker compose -p "$project" --env-file "$scratch/host.env" -f "$compose" -f "$override" "$@"
}
cleanup() {
  result=$?
  if [ -f "$compose" ]; then
    if [ "$result" -ne 0 ]; then
      compose logs --no-color --tail 100 || true
    fi
    compose down --volumes --remove-orphans || true
  fi
  docker image rm "$DDP_TEST_IMAGE" "$project:template" "$archive_image" "$forward_image" >/dev/null 2>&1 || true
  rm -rf "$scratch"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 tests/proving-ground/verify-backup-test.py
python3 tests/proving-ground/update-test.py
python3 tests/proving-ground/publish-test.py
docker build -f deploy/Dockerfile -t "$project:template" .
docker run --rm --entrypoint sh "$project:template" -c '
  test "$(id -u)" = 10001
  test ! -e /src
  test ! -e /opt/ddp/tests
  test ! -e /opt/ddp/client/synthetic.py
  test ! -e /opt/ddp/.env
  ddp validate --json
  python --version
  pg_dump --version
'
python3 tests/proving-ground/prepare.py "$scratch/client"
docker build -f "$scratch/client/deploy/Dockerfile" -t "$DDP_TEST_IMAGE" "$scratch/client"
python3 - "$scratch" "$DDP_TEST_IMAGE" <<'PY'
import secrets
import sys
from pathlib import Path

scratch = Path(sys.argv[1])
values = {name + '_DATABASE_PASSWORD': secrets.token_hex(32)
          for name in ('OWNER', 'API', 'SCHEDULER', 'JOB', 'BACKUP', 'READONLY')}
values |= {'DDP_IMAGE_DIGEST': sys.argv[2], 'POSTGRES_PASSWORD': secrets.token_hex(32),
           'TUNNEL_TOKEN': 'synthetic-token-never-used', 'BACKUP_AGE_IDENTITY': 'host-only-sentinel'}
(scratch / 'host.env').write_text(''.join(f'{key}={value}\n' for key, value in values.items()))
(scratch / 'host.env').chmod(0o600)
(scratch / 'client/.env.scheduler').write_text('SYNTHETIC_API_KEY=synthetic-api-only\n')
PY
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=smtp \
  -addext subjectAltName=DNS:smtp -keyout "$scratch/smtp.key" \
  -out "$scratch/smtp.crt" >/dev/null 2>&1
chmod 600 "$scratch/smtp.key"
compose --profile maintenance config --format json > "$scratch/compose.json"
compose up -d --wait api scheduler
python3 tests/proving-ground/check-compose.py "$scratch/compose.json"
compose logs --no-color postgres > "$scratch/postgres.log"
python3 - "$scratch" <<'PY'
import sys
from pathlib import Path

scratch = Path(sys.argv[1])
log = (scratch / 'postgres.log').read_text()
for line in (scratch / 'host.env').read_text().splitlines():
    key, value = line.split('=', 1)
    if key.endswith('_DATABASE_PASSWORD'):
        assert value not in log, 'provision password leaked through PostgreSQL logging'
assert 'SCRAM-SHA-256$4096:' not in log, 'password verifier leaked through PostgreSQL logging'
assert 'duration:' in log and 'statement:' in log, 'logging leakage check did not enable logging'
PY
compose run --rm --no-deps maintenance migrate up --json
scheduler_database=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["services"]["scheduler"]["environment"]["DATABASE_URL"])' "$scratch/compose.json")
DATABASE_URL="$scheduler_database" compose run --rm --no-deps -e DATABASE_URL maintenance models verify --json
compose exec -T api python - <<'PY'
import urllib.error
import urllib.request

urllib.request.urlopen('http://127.0.0.1:8080/healthz', timeout=3).close()
with urllib.request.urlopen('http://127.0.0.1:9091/metrics', timeout=6) as response:
    metrics = response.read().decode()
for expected in ('ddp_metrics_database_up 1', 'go_goroutines', 'process_cpu_seconds_total',
                 'ddp_http_request_duration_seconds_count{method="GET",route="GET /healthz",status="200"}'):
    if expected not in metrics:
        raise RuntimeError(f'API metrics missing {expected}')
try:
    urllib.request.urlopen('http://127.0.0.1:8080/metrics', timeout=3)
except urllib.error.HTTPError as error:
    if error.code != 404:
        raise
else:
    raise RuntimeError('metrics exposed on the public portal listener')
PY
DDP_BASE_URL=http://localhost:18081 DDP_TEST_EMAIL=operator@example.test \
  DDP_TEST_PASSWORD=synthetic-browser-password-2026 \
  npm exec --prefix frontend --workspace apps/portal -- playwright test --reporter=line

python3 tests/proving-ground/check-operations.py "$scratch" "$project"

python3 tests/proving-ground/check-weekly-restore.py "$scratch/client" "$project" \
  "$scratch/host.env" "$archive_image" "$forward_image"
