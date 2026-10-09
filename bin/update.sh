#!/usr/bin/env bash
set -euo pipefail
umask 077
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
[[ $# -eq 0 ]] || { echo 'usage: bin/update.sh' >&2; exit 1; }
for command in docker jq flock awk; do
  command -v "$command" >/dev/null || { echo "update: missing $command" >&2; exit 1; }
done
[[ -f .env && ! -L .env ]] || { echo 'update: a regular .env is required' >&2; exit 1; }
exec 9>.ddp-update.lock
flock -n 9 || { status=$?; [[ $status -eq 1 ]] && exit 0; exit "$status"; }
export COMPOSE_FILE=${COMPOSE_FILE:-deploy/compose.yaml}
compose() { docker compose --env-file .env "$@"; }
valid_image() { [[ $1 =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$ ]]; }
valid_revision() { [[ $1 =~ ^[a-f0-9]{40}$ ]]; }
die() { echo "update: $*" >&2; exit 1; }
work=$(mktemp -d "$ROOT/.ddp-update.XXXXXX")
containers=()
current='' previous='' failed='' pending='' revision='' phase='preflight' candidate=''
armed=false

write_state() {
  jq -n --arg current "${1-$current}" --arg previous "${2-$previous}" --arg failed "${3-$failed}" \
    --arg pending "${4-$pending}" --arg revision "$revision" --arg phase "$phase" \
    '{version:1,current:$current,previous:$previous,failed:$failed,pending:$pending,revision:$revision,phase:$phase}' \
    >"$work/state" || return 1
  mv -f -- "$work/state" .ddp-release.json
}
write_environment() {
  # Replace only the image; Compose, rather than a shell, parses secret values.
  awk '/^[[:space:]]*(export[[:space:]]+)?DDP_IMAGE_DIGEST[[:space:]]*=/ {next}
       {print} END {print "DDP_IMAGE_DIGEST=" ENVIRON["DDP_IMAGE_DIGEST"]}' .env >"$work/environment" || return 1
  mv -f -- "$work/environment" .env
}
maintenance() {
  local image=$1 role=$2 name
  shift 2
  case "$role" in
    owner) DATABASE_URL=$(jq -er '.services.maintenance.environment.DATABASE_URL' "$work/compose.json") || return 1 ;;
    scheduler) DATABASE_URL=$(jq -er '.services.scheduler.environment.DATABASE_URL' "$work/compose.json") || return 1 ;;
    backup) DATABASE_URL=$(jq -er '.services.scheduler.environment.BACKUP_DATABASE_URL' "$work/compose.json") || return 1 ;;
    none) DATABASE_URL='' ;;
    *) return 1 ;;
  esac
  export DATABASE_URL
  name="ddp-update-$$-$RANDOM"
  containers+=("$name")
  DDP_IMAGE_DIGEST="$image" compose run --name "$name" --rm --no-deps \
    -e DATABASE_URL -e BACKUP_AGE_IDENTITY= maintenance "$@" --json
}
remove_maintenance() {
  local name
  for name in ${containers[@]+"${containers[@]}"}; do
    docker rm -f "$name" >/dev/null 2>&1 || :
  done
  containers=()
}
rollback() {
  export DDP_IMAGE_DIGEST="$current"
  write_environment || return 1
  compose stop scheduler || return 1
  case "$phase" in
    models|startup|rollback)
      compose stop api || return 1
      maintenance "$current" scheduler models apply >"$work/models-rollback.json" || return 1
      ;;
  esac
  compose up -d --no-deps --wait --wait-timeout 120 api || return 1
  compose up -d --no-deps --wait --wait-timeout 120 scheduler || return 1
  maintenance "$current" owner deploy record --current "$current" --image "$pending" \
    --revision "$revision" --status failed --phase "$phase" >"$work/outcome.json" || return 1
  write_state "$current" "$previous" "$pending" '' || return 1
  failed=$pending
  pending=''
  armed=false
  echo "update: restored $current; rejected $failed" >&2
}
cleanup() {
  local status=$?
  trap - EXIT INT TERM HUP
  remove_maintenance
  if [[ $status -ne 0 && $armed == true ]]; then
    echo "update: $phase failed (exit $status); restoring $current" >&2
    if ! rollback; then
      phase=rollback
      failed=$pending
      write_state || :
      echo 'update: rollback incomplete; next invocation will recover the saved previous image' >&2
    fi
  fi
  remove_maintenance
  rm -rf -- "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

if [[ -e .ddp-release.json || -L .ddp-release.json ]]; then
  [[ -f .ddp-release.json && ! -L .ddp-release.json ]] || die 'release state must be a regular file'
  jq -e '.version == 1 and ([.current,.previous,.failed,.pending,.revision,.phase] | all(type == "string"))' \
    .ddp-release.json >/dev/null || die 'invalid release state'
  current=$(jq -r .current .ddp-release.json)
  previous=$(jq -r .previous .ddp-release.json)
  failed=$(jq -r .failed .ddp-release.json)
  pending=$(jq -r .pending .ddp-release.json)
  revision=$(jq -r .revision .ddp-release.json)
  phase=$(jq -r .phase .ddp-release.json)
  valid_image "$current" || die 'invalid saved running image'
  for saved in "$previous" "$failed" "$pending"; do
    [[ -z $saved ]] || { valid_image "$saved" && [[ ${saved%@*} == "${current%@*}" ]]; } || die 'invalid saved image repository'
  done
  case "$phase" in preflight|backup|migrations|models|startup|rollback) ;; *) die 'invalid saved release phase' ;; esac
  [[ -z $pending ]] || valid_revision "$revision" || die 'invalid pending revision'
fi
api_id=$(compose ps -a -q api) || die 'cannot inspect API service'
if [[ -n $api_id ]]; then
  observed=$(docker inspect --format '{{.Config.Image}}' "$api_id") || die 'cannot inspect API image'
  valid_image "$observed" || die 'API must run an immutable GHCR image'
  if [[ -z $current ]]; then
    current=$observed
  elif [[ $observed != "$current" && $observed != "$pending" ]]; then
    die 'API image differs from saved release state'
  fi
elif [[ -z $pending ]]; then
  die 'initialize the API before enabling updates'
fi
export DDP_IMAGE_DIGEST="$current"
compose --profile maintenance config --format json >"$work/compose.json" || die 'cannot resolve maintenance configuration'
if [[ -n $pending ]]; then
  armed=true
  die 'recovering an interrupted release'
fi
write_state
repo=${current%@*}
docker pull "$repo:stable" || die 'cannot pull the stable release'
docker image inspect "$repo:stable" --format '{{json .RepoDigests}}' >"$work/digests.json"
candidate=$(jq -er --arg prefix "$repo@sha256:" '[.[] | select(startswith($prefix))][0] // empty' "$work/digests.json") || die 'stable did not resolve to a digest'
valid_image "$candidate" || die 'invalid resolved image'
# These guards avoid executing unchanged or already rejected image code.
if [[ $candidate == "$current" || $candidate == "$failed" ]]; then
  echo 'update: stable is unchanged or already rejected'
  exit 0
fi
if ! maintenance "$candidate" none version >"$work/version.json" || \
   ! revision=$(jq -er 'select(.version == 1 and .ok == true) | .data.version' "$work/version.json") || \
   ! valid_revision "$revision"; then
  failed=$candidate
  revision=''
  write_state
  die 'candidate lacks a valid embedded revision'
fi
pending=$candidate
phase=preflight
write_state
armed=true
maintenance "$candidate" owner deploy plan --current "$current" --image "$candidate" \
  --failed "$failed" >"$work/plan.json"
jq -e --arg revision "$revision" '.version == 1 and .ok == true and .data.action == "apply" and .data.revision == $revision and (.data.requires_backup | type == "boolean")' \
  "$work/plan.json" >/dev/null || die 'candidate preflight refused the release'
compose stop scheduler
phase=backup
write_state
if jq -e '.data.requires_backup' "$work/plan.json" >/dev/null; then
  maintenance "$current" backup backup run --image "$current" >"$work/backup.json"
  jq -e --arg image "$current" '.version == 1 and .ok == true and .data.status == "succeeded" and .data.backup.image == $image' \
    "$work/backup.json" >/dev/null || die 'fresh backup did not succeed'
fi
phase=migrations
write_state
maintenance "$candidate" owner migrate up >"$work/migrations.json"
phase=models
write_state
maintenance "$candidate" scheduler models apply >"$work/models.json"
phase=startup
write_state
export DDP_IMAGE_DIGEST="$candidate"
write_environment
compose up -d --no-deps --wait --wait-timeout 120 api
compose up -d --no-deps --wait --wait-timeout 120 scheduler
maintenance "$candidate" owner deploy record --current "$current" --image "$candidate" \
  --revision "$revision" --status succeeded --phase ready >"$work/outcome.json"
write_state "$candidate" "$current" "$failed" ''
previous=$current
current=$candidate
pending=''
armed=false
echo "update: activated $current"
