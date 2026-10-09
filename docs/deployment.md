# Production Compose and database credentials

The supported runtime is one Linux host with Docker Compose. The checked-in
[`deploy/compose.yaml`](../deploy/compose.yaml) runs Postgres, the API, the scheduler
and cloudflared. Each dealership runs its own deployment from its own repository:
a copy of this platform created by `ddp init` (see [discovery](discovery.md)) plus
the dealership's own code. Where file, service and variable names below say
"client", they refer to that dealership-owned code and data. The operator is the
person who runs the system for the dealership: normally someone at the dealership,
or an IT provider the dealership hires, with access the dealership grants and can
revoke. Cloudflare routes and Access policies live in the dealership's own
Cloudflare account and are configured there, outside these commands.

## Reviewed image publication

Before the first merge, configure the dealership's GitHub repository to require an
approving review and the Go, Python, Frontend and System checks. From that checkout,
an administrator applies the checked-in [branch policy](../.github/main-protection.json):

```sh
repository=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
gh api --method PUT "repos/$repository/branches/main/protection" \
  --input .github/main-protection.json
gh api "repos/$repository/branches/main/protection"
```

Read back the policy: all four checks must come from GitHub Actions, branches must
be current, and approval must cover the last push. Administrator enforcement must
be enabled, with no review bypass, force pushes or branch deletion. GitHub requires
a supported paid plan for [private repository branch protection](https://docs.github.com/en/rest/branches/branch-protection).
Have another authorized person review the final push before merging.

The Checks workflow publishes only after all four jobs pass on a push to `main`.
Its release job uses the repository's `GITHUB_TOKEN` with `packages: write`; PR jobs
have no registry write permission. The publisher refuses an unprotected branch,
a dirty checkout or a revision different from the event's SHA.

`bin/publish-image.sh` builds `ghcr.io/<owner>/<repository>:<sha>` for Linux AMD64 and
ARM64, embeds that SHA and runs `version --json` on both platforms. It promotes the
built index by digest, preserving both platforms and attestations, then verifies
that `:stable` resolves to the same digest. Main workflows serialize without
canceling the active release. A run whose SHA no longer matches remote `main` skips
publication before building or promotion after building. A failed build or version
check leaves the existing `:stable` pointer in place.

The [GHCR package](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
belongs to the dealership's repository. Configure host access to pull that private
package with read-only package credentials. The release job needs no host, database
or Cloudflare credentials. Merge a corrected commit to publish a new release; the
host updater never retags a failed image.

## Host inputs

Use the dealership's initialized checkout. Copy its `.env.example` to `.env`, set
mode `0600`, and supply these values from the dealership's approved deployment facts:

| Input | Purpose |
|---|---|
| `DDP_IMAGE_DIGEST` | Exact `ghcr.io/<owner>/<repository>@sha256:<digest>` application image |
| `POSTGRES_PASSWORD` | Cluster administrator; initial migration, provisioning and recovery only |
| `OWNER_DATABASE_PASSWORD` | One-shot schema migrations |
| `API_DATABASE_PASSWORD` | Portal database login |
| `SCHEDULER_DATABASE_PASSWORD` | Scheduling, model apply/refresh and operational writes |
| `JOB_DATABASE_PASSWORD` | Python jobs that load the dealership's data |
| `BACKUP_DATABASE_PASSWORD` | Full archive reads and narrow backup-evidence writes |
| `READONLY_DATABASE_PASSWORD` | Read-only inspection, including the installer's host `doctor` run |
| `TUNNEL_TOKEN` | The dealership's remotely managed Cloudflare Tunnel |
| `BACKUP_ACCESS_KEY_ID`, `BACKUP_SECRET_ACCESS_KEY` | This deployment's own off-host bucket |
| `BACKUP_AGE_IDENTITY` | Recovery private key; maintenance only |

Generate every database password independently with `openssl rand -hex 32`.
Provisioning requires exactly 64 lowercase hexadecimal characters. This encoding
also avoids URL and Compose interpolation characters. Store a recovery copy in the
dealership's approved secret storage. Never place populated environment files in Git.

Prepare encrypted host storage. `POSTGRES_DATA_PATH` defaults to
`/var/lib/ddp/postgres`; the Postgres image initializes an empty directory.
`BACKUP_STAGING_PATH` defaults to `/var/lib/ddp/backup-staging`; create it with
owner UID/GID `10001:10001` and mode `0700`. These are the only persistent local
mounts. The application runs as UID 10001 with a read-only root and temporary `/tmp`.

Put only declared integration and SMTP secrets needed by each service in optional
mode-`0600` `.env.api` and `.env.scheduler` files beside `.env`. Do not copy the host
environment into those files. Compose explicitly passes component credentials;
owner/admin passwords, readonly credentials and the recovery key stay outside the
running API and scheduler. See [backup configuration](backups.md) for registry
fields, storage permissions and key custody.

Compose requires `DDP_IMAGE_DIGEST`, but does not validate its format. Use the exact
reviewed image reference above; the host updater validates immutable references.
Configuration and migrations are embedded in that
image, so changing local YAML does not change a running release.

## Initialize a fresh database

Run from the dealership's checkout with its protected environment populated:

```sh
set -a
. ./.env
set +a
compose() { docker compose --env-file .env -f deploy/compose.yaml "$@"; }
compose up -d --wait postgres

export DATABASE_URL="postgres://postgres:${POSTGRES_PASSWORD}@postgres:5432/ddp?sslmode=disable"
compose run --rm -e DATABASE_URL maintenance migrate up --json
for component in owner api scheduler job backup readonly; do
  secret_name="$(printf '%s' "$component" | tr '[:lower:]' '[:upper:]')_DATABASE_PASSWORD"
  compose run --rm -e DATABASE_URL -e "$secret_name" maintenance provision "$component" --json
done
unset DATABASE_URL

compose run --rm maintenance migrate up --json
DATABASE_URL="postgres://ddp_scheduler_login:${SCHEDULER_DATABASE_PASSWORD}@postgres:5432/ddp?sslmode=disable" \
  compose run --rm -e DATABASE_URL maintenance models apply --json
compose up -d --wait api scheduler
```

Initial migration requires the cluster administrator to create the permission
groups. `ddp provision` then creates distinct `ddp_<component>_login` roles and
grants database connection access. Groups remain `NOLOGIN`; each login inherits
exactly its group without permission to grant it. Each login defaults to its
permission group, so migrations create objects owned by `ddp_owner` and model
apply creates relations owned by `ddp_scheduler`.
Provisioning requires a cluster administrator and an existing audit store. It
refuses unsafe group attributes, memberships, or existing login grants/settings.
It changes the password and records `database.provision` in one transaction.
Passwords use SCRAM; the command suppresses native PostgreSQL statement, duration
and transaction-sampling logs on its private connection before changing them.
See [PostgreSQL logging settings](https://www.postgresql.org/docs/17/runtime-config-logging.html).

Apply subsequent migrations with the default owner maintenance credential and
models with the scheduler credential shown above. Bootstrap the first approved operator with the
[account procedure](accounts.md). `/readyz` at `127.0.0.1:8080` gates API startup;
the scheduler waits for API readiness. PostgreSQL has no published port.

Enable the [weekly restore timer](backups.md#enable-weekly-host-verification) after
a verified archive exists and storage, encrypted staging and recovery keys are ready.

Use [release preflight and outcome commands](commands.md#release-preflight-and-outcomes)
for the candidate migration inventory and host release evidence.

## Enable automatic releases

Place the dealership's checkout at `/opt/ddp`, owned by root, with its protected
`.env` and an initialized API running an immutable image. Install Bash, Docker
Compose, `jq`, `flock` and `awk`. Authenticate root's Docker client to GHCR with the
approved read-only package token. The repository's `:stable` tag must identify a
reviewed release with its forty-character Git revision embedded in `ddp version`,
published through the [reviewed image workflow](#reviewed-image-publication).

After a manual update succeeds, install the units:

```sh
cd /opt/ddp
sudo bin/update.sh
sudo install -m 0644 deploy/ddp-update.service deploy/ddp-update.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ddp-update.timer
sudo journalctl -u ddp-update.service -n 100 --no-pager
```

The timer starts after boot and checks again five minutes after each run. The host
lock prevents overlapping invocations. The updater pulls `:stable`, resolves its
digest and asks the candidate's `ddp deploy plan` for its migration inventory.
Pending migrations require a fresh encrypted off-host backup from the current
image. It stops the scheduler, runs owner migrations, applies models as the
scheduler role, updates only `DDP_IMAGE_DIGEST` in `.env`, and starts the API and
scheduler. API readiness gates activation. Recovery credentials are not passed to
release commands.

`.ddp-release.json` records current, previous, failed and pending image digests.
Keep this root-owned state file with the deployment. An interrupted update retains
its pending intent; the next invocation first restores the saved current image.
Startup failure restores that image and its model definitions, records the failure
for `ddp:deployment` health, and rejects that digest on later checks. A failed
rollback keeps its recovery intent and retries recovery on the next invocation.
Inspect the unit journal and `ddp deploy status --json` before intervening.

Rollback preserves applied migrations and leaves the registry tag unchanged.
Publish a corrected release with a new digest to recover from a rejected image.
Changes must remain compatible with the previous release's code and model
definitions. Later business-health failures produce alerts; they do not roll back
a release that passed startup. The updater changes application services only;
host procedures and pinned infrastructure require a reviewed maintenance change.

## Tunnel and rotation

Configure the dealership's remotely managed tunnel, in its own Cloudflare account,
with portal origin `http://localhost:8080` and SSH origin `ssh://localhost:22`.
Cloudflared uses the Linux host network, the `TUNNEL_TOKEN` environment variable
and a metrics listener on `127.0.0.1:20241`. Its image is pinned and automatic
binary updates are disabled. These are supported
[cloudflared runtime settings](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/run-parameters/).
The SSH route is for the dealership's own administrators. Set up its Cloudflare
Access policy with MFA, the administrator account's key and the host firewall
(refusing incoming connections; the tunnel connects outward, so DDP needs none)
before starting the connector with `compose up -d cloudflared`. A running connector alone does not
prove that the access policy or SSH restrictions work.

To rotate a component password, replace that value in the protected host `.env`,
reload it, and run the same administrator `provision <component>` command with its
password environment variable. Restart the affected services with
`compose up -d --force-recreate api` or `scheduler`; job and backup credentials
belong to the scheduler. Existing database sessions survive a password change,
so restart completes the service cutover. Never promote a group itself to `LOGIN`.

`make check-image` initializes a synthetic project and runs this same Compose file
with disposable volume and port overrides. It exercises provisioning, ingestion,
model contracts, authenticated browser access, separate service credentials and
owner maintenance. It also scans PostgreSQL logs with all statement/duration
logging enabled for credential leaks. A disposable Docker host and private TLS
registry exercise real tag promotion, immutable pulls, startup rollback and
encrypted restore. This does not contact Cloudflare; verify the routes and Access
policy on the real deployment.

## Fresh-host installer

`bin/install.sh` is a console-first procedure for a fresh Ubuntu 24.04 systemd host
whose checkout is already at `/opt/ddp`. It refuses an existing `.env`, release
state or protected SSH/systemd paths, so an operator must resolve existing state
manually before retrying. It records the fixed Compose project name
`ddp` in `.env`, which the updater and backup verifier also use. Existing
containers under that project or checkout, and project volumes, block installation.
The host must already have Docker Engine,
Compose v2, `jq`, `flock`, OpenSSL, OpenSSH server and systemd. The procedure does
not install packages or make Cloudflare API calls.

Run it locally at the host console as root. It collects the exact reviewed image,
GHCR read-only package token, operator bootstrap credentials, backup inputs,
Cloudflare `TUNNEL_TOKEN`, a completed source-checkout/CI smoke evidence path and
the name of an existing non-root local administrator account. It generates each
six-component and Postgres password independently, writes a root-owned mode-`0600` `.env`, initializes and
provisions the database, applies models, bootstraps the first DDP operator, starts
the exact image, checks readiness, verifies models, records the initial deployment,
creates an encrypted backup, and runs the existing retained backup verification.
It then extracts the exact image binary to a protected temporary directory and runs
`doctor` on the host with the readonly database login. The full report must be
healthy; it remains in `/var/lib/ddp/install-state/doctor.json`. The checkout HEAD
must match the image revision, and Git must contain the recorded template revision
with unchanged platform files. Production images do not contain the
source/Node/Playwright smoke toolchain; that smoke gate must be complete before
installation.

The installer creates no accounts. Only the named administrator account may sign
in over SSH (`AllowUsers`); set up its key and sudo access before installing. The
installer configures sshd for loopback listeners only, key-only authentication,
no password or keyboard-interactive login, no root login and no user environment
files, and handles Ubuntu socket activation. It then installs the checked-in
updater and weekly backup-verification units. The Compose `cloudflared` service
is the only connector; it uses the supplied `TUNNEL_TOKEN` and the pinned image. The
installer does not create a second host connector or configure Cloudflare
routes/Access. Before enabling maintenance timers, it waits for cloudflared's
loopback `/ready` endpoint to report an edge connection. A started container alone
is insufficient. This [readiness check](https://github.com/cloudflare/cloudflared/blob/master/metrics/readiness.go)
does not prove the public routes or Access policy; verify those remotely.

The Postgres path must already be an empty, non-symlink directory. The backup
staging path must already be externally encrypted, non-symlink, owned by UID/GID
`10001:10001` and mode `0700`; the installer refuses to infer encryption from a
pathname or repair unsafe paths. Values serialized into `.env` accept only ASCII
letters, digits and `_./:@+=,-`; unsupported characters fail before file creation.

Before running the installer, configure the separate portal and SSH routes in the
dealership's Cloudflare account. Protect the SSH route with the dealership's own
Cloudflare Access policy requiring MFA. The portal is not placed behind Cloudflare
Access; DDP's own account sign-in protects it. The host is not ready for remote use
until an approved operator verifies portal login, the SSH Access policy with MFA,
the administrator account's key and loopback-only port 22.

`make check-system` runs [`tests/check-install.sh`](../tests/check-install.sh),
including in generated projects. It checks actual validators, existing Compose
state refusal, repository digest aliases, SSH allow-list output, and timer refusal
when tunnel readiness fails. On a root Linux host with OpenSSH server available,
it also parses the actual sshd fragment and checks its effective restrictions.
