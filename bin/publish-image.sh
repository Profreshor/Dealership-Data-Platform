#!/usr/bin/env bash
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."

fail() { printf 'publish: %s\n' "$*" >&2; exit 1; }
[[ $# == 0 ]] || fail 'no arguments are accepted'
[[ ${GITHUB_EVENT_NAME:-} == push && ${GITHUB_REF:-} == refs/heads/main &&
   ${GITHUB_REF_PROTECTED:-} == true ]] || fail 'a push to protected main is required'
[[ ${GITHUB_REPOSITORY:-} =~ ^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$ ]] || fail 'invalid repository'
[[ ${GITHUB_SHA:-} =~ ^[a-f0-9]{40}$ ]] || fail 'invalid revision'
[[ $(git rev-parse HEAD) == "$GITHUB_SHA" ]] || fail 'checkout does not match the release revision'
status=$(git status --porcelain) || fail 'cannot inspect release checkout'
[[ -z $status ]] || fail 'release checkout must be clean'
image="ghcr.io/$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]')"

current_main() {
  local revision
  revision=$(git ls-remote --exit-code origin refs/heads/main | awk '{print $1}')
  [[ $revision =~ ^[a-f0-9]{40}$ ]] || fail 'cannot resolve main'
  if [[ $revision != "$GITHUB_SHA" ]]; then
    printf 'publish: main has advanced; skipping %s\n' "$GITHUB_SHA" >&2
    exit 0
  fi
}
current_main
work=$(mktemp -d "${TMPDIR:-/tmp}/ddp-publish.XXXXXX")
container="ddp-publish-$(basename "$work" | tr '[:upper:]' '[:lower:]')"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker buildx build --platform linux/amd64,linux/arm64 --provenance=true \
  --build-arg "REVISION=$GITHUB_SHA" \
  --label "org.opencontainers.image.source=https://github.com/$GITHUB_REPOSITORY" \
  --label "org.opencontainers.image.revision=$GITHUB_SHA" \
  --tag "$image:$GITHUB_SHA" --push --metadata-file "$work/build.json" \
  -f deploy/Dockerfile .
digest=$(jq -er '."containerimage.digest" | select(test("^sha256:[a-f0-9]{64}$"))' "$work/build.json")
for platform in linux/amd64 linux/arm64; do
  timeout 60 docker run --rm --name "$container" --platform "$platform" \
    --network none --read-only --cap-drop ALL "$image@$digest" version --json > "$work/version.json"
  jq -e --arg revision "$GITHUB_SHA" \
    '.version == 1 and .ok == true and .data.version == $revision' "$work/version.json" >/dev/null ||
    fail "image revision mismatch on $platform"
done
current_main
# Preserve the complete index, including both platforms and their attestations.
docker buildx imagetools create --tag "$image:stable" "$image@$digest"
[[ $(docker buildx imagetools inspect "$image:stable" --format '{{.Manifest.Digest}}') == "$digest" ]] ||
  fail 'stable does not match the built digest'
printf 'publish: %s@%s\n' "$image" "$digest"
