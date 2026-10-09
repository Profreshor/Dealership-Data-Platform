#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

for command in docker jq flock; do
  command -v "$command" >/dev/null 2>&1 || { echo "verify-backup: missing $command" >&2; exit 1; }
done
[[ -f .env ]] || { echo "verify-backup: .env is required" >&2; exit 1; }

exec 9>".ddp-backup-verify.lock"
flock -n 9 || { status=$?; [[ $status -eq 1 ]] && exit 0; exit "$status"; }

export COMPOSE_FILE=${COMPOSE_FILE:-deploy/compose.yaml}
compose() { docker compose --env-file .env "$@"; }

work=$(mktemp -d "${TMPDIR:-/tmp}/ddp-backup-verify.XXXXXX")
containers=()
cleanup() {
  local status=$?
  local name
  for name in ${containers[@]+"${containers[@]}"}; do
    docker rm -f "$name" >/dev/null 2>&1 || :
  done
  rm -rf -- "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

die() { echo "verify-backup: $*" >&2; exit 1; }
valid_image() { [[ $1 =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$ ]]; }
valid_revision() { [[ $1 =~ ^[a-f0-9]{40}$ ]]; }

api_id=$(compose ps -q api) || die "cannot inspect api service"
[[ -n $api_id ]] || die "api service is not running"
current=$(docker inspect --format '{{.Config.Image}}' "$api_id") || die "cannot inspect running api image"
valid_image "$current" || die "running api image is not an immutable GHCR digest"
current_repo=${current%@*}
export DDP_IMAGE_DIGEST="$current"

compose --profile maintenance config --format json >"$work/compose.json" || die "cannot resolve maintenance compose config"
list_name="ddp-backup-verify-list-$$-$RANDOM"
containers+=("$list_name")
compose run --name "$list_name" --rm --no-deps maintenance backup list --json >"$work/list.json" || die "backup list failed"

jq -e '(.version == 1 and .ok == true and (.data | type == "array") and (.data | length > 0))' "$work/list.json" >/dev/null || die "backup list returned an invalid or empty envelope"
id=$(jq -er '.data[0].id // empty' "$work/list.json") || die "backup manifest ID missing"
image=$(jq -er '.data[0].image // empty' "$work/list.json") || die "backup manifest image missing"
revision=$(jq -er '.data[0].revision // empty' "$work/list.json") || die "backup manifest revision missing"
valid_image "$image" || die "backup manifest image is not an immutable GHCR digest"
valid_revision "$revision" || die "backup manifest revision is invalid"
manifest_repo=${image%@*}
[[ $manifest_repo == "$current_repo" ]] || die "backup manifest belongs to a different image repository"

if ! docker image inspect "$image" >/dev/null 2>&1; then
  docker pull "$image" || die "cannot pull backup image"
fi
export DDP_IMAGE_DIGEST="$image"

version_name="ddp-backup-verify-version-$$-$RANDOM"
containers+=("$version_name")
compose run --name "$version_name" --rm --no-deps maintenance version --json >"$work/version.json" || die "backup image version check failed"
jq -e --arg revision "$revision" '(.version == 1 and .ok == true and .data.version == $revision)' "$work/version.json" >/dev/null || die "backup image revision does not match manifest"

DATABASE_URL=$(jq -er '"postgres://postgres:" + (.services.postgres.environment.POSTGRES_PASSWORD // error("postgres password missing") | @uri) + "@postgres:5432/ddp?sslmode=disable"' "$work/compose.json") || die "cannot derive postgres admin URL"

export DATABASE_URL

restore_name="ddp-backup-verify-restore-$$-$RANDOM"
containers+=("$restore_name")
compose run --name "$restore_name" --rm --no-deps -e DATABASE_URL maintenance backup restore "$id" --verify --if-due --json
