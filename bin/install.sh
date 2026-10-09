#!/usr/bin/env bash
set -euo pipefail
umask 077
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C COMPOSE_PROJECT_NAME=ddp

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
STATE_DIR=/var/lib/ddp/install-state
STATE_FILE=$STATE_DIR/phase
phase=preflight
tmp=''
doctor_container=''
state_owned=false

die() { echo "install: $*" >&2; exit 1; }
failed() {
  status=$?
  if [[ $status -ne 0 ]]; then
    if [[ $state_owned == true ]]; then
      mkdir -p "$STATE_DIR" 2>/dev/null || :
      printf 'failed:%s\n' "$phase" >"$STATE_FILE" 2>/dev/null || :
      chmod 0700 "$STATE_DIR" 2>/dev/null || :
    fi
    echo "install: stopped during $phase; evidence: $STATE_FILE" >&2
  fi
  [[ -z $doctor_container ]] || docker rm -f "$doctor_container" >/dev/null 2>&1 || :
  [[ -z $tmp ]] || rm -rf -- "$tmp"
  exit "$status"
}
trap failed EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

require_command() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }
ask() {
  local name=$1 prompt=$2 value
  while :; do
    IFS= read -r -p "$prompt: " value || die 'input ended'
    [[ -n $value ]] && { printf -v "$name" '%s' "$value"; return; }
    echo 'A value is required.' >&2
  done
}
ask_secret() {
  local name=$1 prompt=$2 value
  while :; do
    IFS= read -r -s -p "$prompt: " value || die 'input ended'
    printf '\n' >&2
    [[ -n $value ]] && { printf -v "$name" '%s' "$value"; unset value; return; }
    echo 'A value is required.' >&2
  done
}
ask_path() {
  local name=$1 prompt=$2 value
  while :; do
    IFS= read -r -p "$prompt: " value || die 'input ended'
    [[ -n $value ]] && break
    echo 'A value is required.' >&2
  done
  [[ $value == /* && $value != *$'\n'* ]] || die "$name must be an absolute path"
  printf -v "$name" '%s' "$value"
}
valid_hex() { [[ $1 =~ ^[a-f0-9]{64}$ ]]; }
valid_digest() { [[ $1 =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$ ]]; }
# The public template's own package is never a dealership release channel.
valid_own_image() { local repo=${1%@*}; [[ $repo != ghcr.io/profreshor/dealership-data-platform && $repo != ghcr.io/profreshor/dealership-data-platform/* ]]; }
valid_email() { [[ $1 =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; }
valid_env() { [[ $1 =~ ^[A-Za-z0-9_./:@+=,-]+$ ]]; }
valid_user() { [[ $1 =~ ^[a-z_][a-z0-9_-]{0,31}$ && $1 != root ]]; }
compose() { docker compose --env-file .env -f deploy/compose.yaml "$@"; }
verify_image_digest() { docker image inspect "$1" --format '{{range .RepoDigests}}{{println .}}{{end}}' | grep -Fx -- "$1" >/dev/null; }
verify_ssh_users() { [[ $(awk '$1 == "allowusers" {for (i=2; i<=NF; i++) print $i}' | sort) == "$1" ]]; }
write_state() { mkdir -p "$STATE_DIR"; chmod 0700 "$STATE_DIR"; printf '%s\n' "$phase" >"$STATE_FILE"; chmod 0600 "$STATE_FILE"; }

check_existing_compose() {
  local filter existing
  for filter in label=com.docker.compose.project=ddp label=com.docker.compose.project.working_dir=/opt/ddp label=com.docker.compose.project.working_dir=/opt/ddp/deploy; do
    existing=$(docker ps -aq --filter "$filter")
    [[ -z $existing ]] || die 'existing DDP Compose containers require human review'
  done
  existing=$(docker volume ls -q --filter label=com.docker.compose.project=ddp)
  [[ -z $existing ]] || die 'existing DDP Compose volumes require human review'
}

[[ $EUID -eq 0 ]] || die 'run as root on the production Ubuntu host'
[[ $ROOT == /opt/ddp ]] || die 'the checkout must be /opt/ddp'
[[ -d .git && -f deploy/compose.yaml && -f bin/update.sh && -f bin/verify-backup.sh ]] || die 'incomplete DDP checkout'
[[ $(stat -c '%u' .) == 0 ]] || die '/opt/ddp must be root-owned'

for command in bash curl docker flock jq openssl sshd systemctl install stat id realpath ss git; do require_command "$command"; done
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required'
systemctl is-system-running >/dev/null 2>&1 || [[ $(systemctl is-system-running 2>/dev/null || true) == degraded ]] || die 'systemd is required'
docker info >/dev/null 2>&1 || die 'Docker daemon is unavailable'
[[ -S /var/run/docker.sock ]] || die 'Docker socket is required'

[[ ! -e $STATE_DIR && ! -L $STATE_DIR ]] || die 'prior installer state/evidence exists; refusing to overwrite it'
mkdir -p "$STATE_DIR"
state_owned=true
write_state
check_existing_compose

[[ ! -e .env && ! -L .env ]] || die '.env already exists; refusing to overwrite it'
[[ ! -e .ddp-release.json && ! -L .ddp-release.json ]] || die '.ddp-release.json already exists; refusing existing deployment state'
for path in /etc/systemd/system/ddp-update.service /etc/systemd/system/ddp-update.timer /etc/systemd/system/ddp-backup-verify.service /etc/systemd/system/ddp-backup-verify.timer /etc/ssh/sshd_config.d/90-ddp.conf; do
  [[ ! -e $path && ! -L $path ]] || die "existing protected path requires human review: $path"
done
systemctl cat ssh.service >/dev/null 2>&1 || die 'Ubuntu ssh.service is required'
[[ ! -e /etc/systemd/system/ssh.socket ]] || die 'custom ssh.socket requires human review'

phase=collect
tmp=$(mktemp -d /tmp/ddp-install.XXXXXX)
chmod 0700 "$tmp"
image='' ghcr_user='' ghcr_token='' operator_email='' bootstrap_password=''
tunnel_token='' backup_key_id='' backup_secret='' backup_identity=''
postgres_path='' staging_path='' ssh_user='' smoke_evidence='' encryption_ack=''
ask image 'Exact reviewed DDP image digest (ghcr.io/...@sha256:...)'
valid_digest "$image" || die 'image must be an exact lowercase GHCR sha256 digest'
valid_own_image "$image" || die "image is from the public DDP template package; it must be built and published by the dealership's own repository"
ask ghcr_user 'GHCR read-only package username'
ask_secret ghcr_token 'GHCR read-only package token'
ask operator_email 'Initial DDP operator email'
valid_email "$operator_email" || die 'operator email is invalid'
ask_secret bootstrap_password 'Initial DDP operator password (8-128 bytes)'
(( ${#bootstrap_password} >= 8 && ${#bootstrap_password} <= 128 )) || die 'operator password must be 8-128 bytes'
ask_secret tunnel_token 'Cloudflare Tunnel token for the pinned Compose cloudflared service'
ask backup_key_id 'Backup object-store access key ID'
ask_secret backup_secret 'Backup object-store secret access key'
ask_secret backup_identity 'Backup AGE identity (private key; stored for maintenance only)'
ask_path postgres_path 'Encrypted Postgres data path'
ask_path staging_path 'Encrypted backup staging path'
ask smoke_evidence 'Path to completed source-checkout/CI smoke evidence'
[[ -f $smoke_evidence && ! -L $smoke_evidence ]] || die 'source-checkout/CI smoke evidence must be an existing regular file'
ask encryption_ack 'Type PREPARED to confirm both data paths are externally encrypted and approved'
[[ $encryption_ack == PREPARED ]] || die 'encrypted storage preparation is required before installation'
ask ssh_user 'Existing local administrator account allowed to sign in over SSH (not root)'
valid_user "$ssh_user" || die 'SSH account must be a valid non-root local user name'
ssh_uid=$(id -u "$ssh_user" 2>/dev/null) || die "SSH account does not exist: $ssh_user"
[[ $ssh_uid != 0 ]] || die 'SSH account must not have UID 0'
unset ssh_uid

for value in "$image" "$ghcr_user" "$tunnel_token" "$backup_key_id" "$backup_secret" "$backup_identity" "$postgres_path" "$staging_path"; do
  valid_env "$value" || die 'environment values may not contain whitespace, $, #, CR or LF'
done
[[ ! -L $postgres_path && ! -L $staging_path ]] || die 'data paths may not be symlinks'
postgres_real=$(realpath -e "$postgres_path") || die 'Postgres data path must already exist'
staging_real=$(realpath -e "$staging_path") || die 'backup staging path must already exist'
[[ -d $postgres_real && -d $staging_real ]] || die 'data paths must be directories'
[[ $postgres_real != "$staging_real" && $postgres_real != "$staging_real"/* && $staging_real != "$postgres_real"/* ]] || die 'data paths must not overlap'
[[ -z $(find "$postgres_real" -mindepth 1 -maxdepth 1 -print -quit) ]] || die 'Postgres data path must be empty'
[[ $(stat -c '%u:%g:%a' "$staging_real") == 10001:10001:700 ]] || die 'backup staging path must be UID/GID 10001 and mode 0700'

phase=environment
for variable in POSTGRES_PASSWORD OWNER_DATABASE_PASSWORD API_DATABASE_PASSWORD SCHEDULER_DATABASE_PASSWORD JOB_DATABASE_PASSWORD BACKUP_DATABASE_PASSWORD READONLY_DATABASE_PASSWORD; do
  password=$(openssl rand -hex 32)
  valid_hex "$password" || die 'openssl did not generate the required password form'
  printf -v "$variable" '%s' "$password"
  unset password
done
cat >"$tmp/env" <<EOF
COMPOSE_PROJECT_NAME=ddp
DDP_IMAGE_DIGEST=$image
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
OWNER_DATABASE_PASSWORD=$OWNER_DATABASE_PASSWORD
API_DATABASE_PASSWORD=$API_DATABASE_PASSWORD
SCHEDULER_DATABASE_PASSWORD=$SCHEDULER_DATABASE_PASSWORD
JOB_DATABASE_PASSWORD=$JOB_DATABASE_PASSWORD
BACKUP_DATABASE_PASSWORD=$BACKUP_DATABASE_PASSWORD
READONLY_DATABASE_PASSWORD=$READONLY_DATABASE_PASSWORD
TUNNEL_TOKEN=$tunnel_token
BACKUP_ACCESS_KEY_ID=$backup_key_id
BACKUP_SECRET_ACCESS_KEY=$backup_secret
BACKUP_AGE_IDENTITY=$backup_identity
POSTGRES_DATA_PATH=$postgres_path
BACKUP_STAGING_PATH=$staging_path
EOF
install -o root -g root -m 0600 "$tmp/env" .env
export POSTGRES_PASSWORD OWNER_DATABASE_PASSWORD API_DATABASE_PASSWORD SCHEDULER_DATABASE_PASSWORD JOB_DATABASE_PASSWORD BACKUP_DATABASE_PASSWORD READONLY_DATABASE_PASSWORD
unset tunnel_token backup_secret backup_identity encryption_ack

phase=registry
printf '%s' "$ghcr_token" | docker login ghcr.io --username "$ghcr_user" --password-stdin >/dev/null
unset ghcr_user ghcr_token
docker pull "$image" >/dev/null
verify_image_digest "$image" || die 'pulled image did not resolve to the requested digest'
revision=$(compose run --rm --no-deps api version --json | jq -er '.data.version // .version' | grep -E '^[0-9a-f]{40}$') || die 'image did not report an embedded Git revision'
checkout_revision=$(git rev-parse HEAD) || die 'cannot read the checkout revision'
[[ $checkout_revision == "$revision" ]] || die 'checkout HEAD must match the embedded image revision before doctor'
phase=database
compose up -d --wait --wait-timeout 120 postgres
admin_url="postgres://postgres:${POSTGRES_PASSWORD}@postgres:5432/ddp?sslmode=disable"
DATABASE_URL=$admin_url compose run --rm --no-deps -e DATABASE_URL maintenance migrate up --json
for component in owner api scheduler job backup readonly; do
  secret_name=$(printf '%s' "$component" | tr '[:lower:]' '[:upper:]')_DATABASE_PASSWORD
  DATABASE_URL=$admin_url compose run --rm --no-deps -e DATABASE_URL -e "$secret_name" maintenance provision "$component" --json
done
unset admin_url
compose run --rm --no-deps maintenance migrate up --json
scheduler_url="postgres://ddp_scheduler_login:${SCHEDULER_DATABASE_PASSWORD}@postgres:5432/ddp?sslmode=disable"
DATABASE_URL=$scheduler_url compose run --rm --no-deps -e DATABASE_URL maintenance models apply --json
unset scheduler_url

phase=bootstrap
api_url="postgres://ddp_api_login:${API_DATABASE_PASSWORD}@postgres:5432/ddp?sslmode=disable"
DDP_BOOTSTRAP_PASSWORD=$bootstrap_password DATABASE_URL=$api_url compose run --rm --no-deps -e DATABASE_URL -e DDP_BOOTSTRAP_PASSWORD api users bootstrap --email "$operator_email" --json
unset api_url operator_email bootstrap_password

phase=startup
compose up -d --wait --wait-timeout 180 api scheduler
curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null

phase=local-checks
models_url="postgres://ddp_scheduler_login:${SCHEDULER_DATABASE_PASSWORD}@postgres:5432/ddp?sslmode=disable"
DATABASE_URL=$models_url compose run --rm --no-deps -e DATABASE_URL maintenance models verify --json
unset models_url
owner_url="postgres://ddp_owner_login:${OWNER_DATABASE_PASSWORD}@postgres:5432/ddp?sslmode=disable"
DATABASE_URL=$owner_url compose run --rm --no-deps -e DATABASE_URL maintenance deploy record --current "$image" --image "$image" --revision "$revision" --status succeeded --phase ready --json
unset owner_url
jq -n --arg image "$image" --arg revision "$revision" '{version:1,current:$image,previous:$image,failed:"",pending:"",revision:$revision,phase:"startup"}' >"$tmp/release"
install -o root -g root -m 0600 "$tmp/release" .ddp-release.json

phase=backup
backup_url="postgres://ddp_backup_login:${BACKUP_DATABASE_PASSWORD}@postgres:5432/ddp?sslmode=disable"
DATABASE_URL=$backup_url compose run --rm --no-deps -e DATABASE_URL maintenance backup run --revision "$revision" --image "$image" --json
unset backup_url
./bin/verify-backup.sh

phase=doctor
pg_container=$(compose ps -q postgres) || die 'cannot inspect Postgres container'
[[ -n $pg_container ]] || die 'Postgres container is not running'
pg_ip=$(docker inspect --format '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$pg_container") || die 'cannot inspect Postgres network address'
[[ $pg_ip =~ ^[0-9]+(\.[0-9]+){3}$ ]] || die 'Postgres container has no usable internal IPv4 address'
compose --profile maintenance config --format json >"$tmp/compose.json"
doctor_container=$(docker create "$image")
docker cp "$doctor_container:/usr/local/bin/ddp" "$tmp/ddp"
docker rm "$doctor_container" >/dev/null
doctor_container=''
chmod 0755 "$tmp/ddp"
doctor_command=(env -i PATH=/usr/local/bin:/usr/bin:/bin HOME=/root)
while IFS= read -r -d '' key && IFS= read -r -d '' value; do
  [[ $key =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die 'Compose produced an invalid doctor environment name'
  doctor_command+=("$key=$value")
done < <(jq -j '[.services.api.environment,.services.scheduler.environment,.services.maintenance.environment] | map(select(. != null)) | add | to_entries[] | select(.key != "DATABASE_URL" and .key != "TMPDIR" and .key != "PATH" and .key != "HOME") | .key + "\u0000" + (.value // "" | tostring) + "\u0000"' "$tmp/compose.json")
doctor_command+=("DATABASE_URL=postgres://ddp_readonly_login:${READONLY_DATABASE_PASSWORD}@${pg_ip}:5432/ddp?sslmode=disable")
doctor_status=0
(cd "$ROOT" && "${doctor_command[@]}" "$tmp/ddp" doctor --config ddp.yaml --json >"$tmp/doctor.json") || doctor_status=$?
install -o root -g root -m 0600 "$tmp/doctor.json" "$STATE_DIR/doctor.json"
(( doctor_status == 0 )) || die 'doctor reported an unhealthy or unknown host state; inspect install-state/doctor.json'
jq -e '.version == 1 and .ok == true and .data.state == "ok"' "$STATE_DIR/doctor.json" >/dev/null || die 'doctor report was not healthy; inspect install-state/doctor.json'

phase=host-access
install -d -o root -g root -m 0755 /run/sshd
cat >"$tmp/sshd" <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
PermitUserEnvironment no
ListenAddress 127.0.0.1
ListenAddress ::1
EOF
printf 'AllowUsers %s\n' "$ssh_user" >>"$tmp/sshd"
install -o root -g root -m 0644 "$tmp/sshd" /etc/ssh/sshd_config.d/90-ddp.conf
sshd -t
ssh_effective=$(sshd -T -C "user=$ssh_user,addr=127.0.0.1,host=localhost")
grep -qx 'passwordauthentication no' <<<"$ssh_effective" || die 'effective sshd password authentication is not disabled'
grep -qx 'kbdinteractiveauthentication no' <<<"$ssh_effective" || die 'effective sshd keyboard-interactive authentication is not disabled'
grep -qx 'permitrootlogin no' <<<"$ssh_effective" || die 'effective sshd root login is not disabled'
grep -qx 'permituserenvironment no' <<<"$ssh_effective" || die 'effective sshd user environment is not disabled'
verify_ssh_users "$ssh_user" <<<"$ssh_effective" || die 'effective sshd AllowUsers is unsafe'
listeners=$(grep '^listenaddress ' <<<"$ssh_effective" | awk '{print $2}' | sort)
[[ $listeners == $'127.0.0.1:22\n[::1]:22' ]] || die 'effective sshd listeners are not loopback-only'
systemctl disable --now ssh.socket >/dev/null 2>&1 || :
systemctl enable ssh.service
# Ubuntu's notify service waits for listeners; reload only queues a SIGHUP.
systemctl restart ssh.service
actual_listeners=$(ss -H -ltn | awk '$4 ~ /:22$/ {print $4}' | sort)
[[ $actual_listeners == $'127.0.0.1:22\n[::1]:22' ]] || die 'actual SSH listeners are not loopback-only'
unset ssh_effective listeners actual_listeners ssh_user

phase=systemd
for unit in ddp-update.service ddp-update.timer ddp-backup-verify.service ddp-backup-verify.timer; do
  install -o root -g root -m 0644 "deploy/$unit" "/etc/systemd/system/$unit"
done
systemctl daemon-reload

phase=remote-transport
compose up -d --wait --wait-timeout 120 cloudflared
curl --fail --silent --show-error --max-time 5 --retry 60 --retry-delay 2 \
  --retry-connrefused --retry-max-time 120 http://127.0.0.1:20241/ready >/dev/null || die 'Cloudflare Tunnel did not become ready; maintenance timers remain disabled'
systemctl enable --now ddp-update.timer
systemctl enable --now ddp-backup-verify.timer

phase=remote-acceptance
cat >&2 <<'EOF'
install: host bootstrap completed. The dealership's own Cloudflare account must publish the portal and SSH routes, with an Access policy requiring MFA on the SSH route.
install: portal authentication remains DDP portal authentication; Cloudflare Access was not applied to the portal.
install: do not declare deployment complete until remote portal and SSH acceptance passes with the dealership's approved identities and keys.
EOF
phase=complete
write_state
echo 'install: complete; remote Cloudflare acceptance is still required' >&2
