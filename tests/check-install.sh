#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
script=$ROOT/bin/install.sh
bash -n "$script"

# Read the actual pure validators without executing host installation.
eval "$(sed -n '/^valid_.*() /p' "$script")"
valid_digest "ghcr.io/acme/client@sha256:$(printf 'a%.0s' {1..64})"
valid_own_image "ghcr.io/acme/client@sha256:$(printf 'a%.0s' {1..64})"
valid_own_image "ghcr.io/acme/dealership-data-platform@sha256:$(printf 'a%.0s' {1..64})"
for value in "ghcr.io/profreshor/dealership-data-platform@sha256:$(printf 'a%.0s' {1..64})" \
  "ghcr.io/profreshor/dealership-data-platform/api@sha256:$(printf 'a%.0s' {1..64})"; do
  if valid_own_image "$value"; then echo 'accepted the public template image' >&2; exit 1; fi
done
valid_env 'token_ABC-123=ok/+:,@.'
valid_hex "$(printf 'a%.0s' {1..64})"
valid_email 'operator@example.test'
valid_user 'itadmin'
valid_user '_svc-admin2'
for value in '' 'root' 'Admin' '1admin' 'it admin' 'admin;id' $'admin\nroot'; do
  if valid_user "$value"; then echo 'accepted unsafe SSH account name' >&2; exit 1; fi
done
for value in 'ghcr.io/acme/client:stable' 'ghcr.io/acme/client@sha256:abc' 'http://example.test'; do
  if valid_digest "$value"; then echo 'accepted invalid image' >&2; exit 1; fi
done
# These are literal hostile input values.
# shellcheck disable=SC2016
for value in '' 'token$ABC' 'token#ABC' 'token with-space' 'token`id`' "token'quoted" 'token;id' $'token\nvalue' $'token\rvalue'; do
  if valid_env "$value"; then echo 'accepted unsupported environment value' >&2; exit 1; fi
done

# Native Compose labels the working directory /opt/ddp/deploy, not /opt/ddp.
eval "$(sed -n '/^check_existing_compose() {$/,/^}$/p' "$script")"
eval "$(sed -n '/^die() /p' "$script")"
# Invoked by the installer function loaded above.
# shellcheck disable=SC2329
docker() {
  if [[ $1 == image && $2 == inspect ]]; then
    printf '%s\n' "local-alias@sha256:$digest" "$test_image"
    return
  fi
  case "$*:$fixture" in
    'ps -aq --filter label=com.docker.compose.project=ddp:project' | \
    'ps -aq --filter label=com.docker.compose.project.working_dir=/opt/ddp/deploy:directory' | \
    'volume ls -q --filter label=com.docker.compose.project=ddp:volume') printf 'existing-resource\n' ;;
  esac
}
# An image can have several repository aliases; the requested one need not be first.
eval "$(sed -n '/^verify_image_digest() /p' "$script")"
digest=$(printf 'a%.0s' {1..64})
test_image="ghcr.io/acme/client@sha256:$digest"
verify_image_digest "$test_image"
if verify_image_digest "ghcr.io/acme/missing@sha256:$digest"; then
  echo 'accepted an absent repository digest' >&2
  exit 1
fi
fixture=empty
check_existing_compose
for fixture in project directory volume; do
  if (check_existing_compose) >/dev/null 2>&1; then
    echo "accepted existing Compose $fixture" >&2
    exit 1
  fi
done
unset -f docker

# A copy outside /opt/ddp must refuse before any host mutation, even as root.
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir "$work/bin"
cp "$script" "$work/bin/install.sh"
if bash "$work/bin/install.sh" </dev/null >"$work/out" 2>"$work/error"; then
  echo 'installer accepted an unsupported checkout' >&2
  exit 1
fi
grep -Eq 'run as root|checkout must be /opt/ddp' "$work/error"
# A started container is insufficient: disconnected tunnels must block the timers.
cat >"$work/transport" <<'EOF'
set -euo pipefail
compose() { :; }
curl() { return "$curl_status"; }
systemctl() { echo timer-enabled; }
die() { exit 1; }
EOF
sed -n '/^phase=remote-transport$/,/^phase=remote-acceptance$/p' "$script" >>"$work/transport"
code=0
curl_status=7 bash "$work/transport" >"$work/transport-out" 2>&1 || code=$?
[[ $code != 0 && ! -s $work/transport-out ]] || { echo 'disconnected tunnel enabled timers' >&2; exit 1; }
curl_status=0 bash "$work/transport" >"$work/transport-out"
[[ $(wc -l <"$work/transport-out" | tr -d ' ') == 2 ]]
# Exercise the installer check against OpenSSH's one-entry-per-line output.
eval "$(sed -n '/^verify_ssh_users() /p' "$script")"
verify_ssh_users itadmin <<<'allowusers itadmin'
verify_ssh_users itadmin <<<$'permitrootlogin no\nallowusers itadmin'
for value in '' 'allowusers other' 'allowusers itadmin root' $'allowusers itadmin\nallowusers other'; do
  if verify_ssh_users itadmin <<<"$value"; then
    echo 'accepted unsafe SSH users' >&2
    exit 1
  fi
done
# Parse the actual sshd fragment when the host has OpenSSH server installed.
if command -v sshd >/dev/null 2>&1 && [[ $EUID -eq 0 && -d /run/sshd ]]; then
  sed -n '/^PasswordAuthentication no$/,/^EOF$/p' "$script" | sed '$d' >"$work/sshd.conf"
  printf 'AllowUsers itadmin\n' >>"$work/sshd.conf"
  sshd -T -f "$work/sshd.conf" -C user=itadmin,addr=127.0.0.1,host=localhost >"$work/sshd-effective"
  grep -qx 'passwordauthentication no' "$work/sshd-effective"
  grep -qx 'kbdinteractiveauthentication no' "$work/sshd-effective"
  grep -qx 'permitrootlogin no' "$work/sshd-effective"
  grep -qx 'permituserenvironment no' "$work/sshd-effective"
  verify_ssh_users itadmin <"$work/sshd-effective"
fi
echo 'check-install: actual validators and early host refusal passed'
