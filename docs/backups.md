# Backups and restore verification

`ddp backup run` creates a custom-format Postgres dump, reads every archived data
block with `pg_restore`, encrypts the file with age, uploads it over HTTPS and reads
it back to verify its SHA-256 and length. Only then does it publish a manifest.
`ddp backup list --json` reads those off-host manifests, including the exact Git
revision of the dealership's repository and the application image digest needed
for recovery.

## Configure storage and keys

Add `deploy.backup` to the deployment's `ddp.yaml` with these public facts:

| Field | Value |
|---|---|
| `endpoint` | HTTPS origin of the dealership's S3-compatible storage service |
| `region` | Storage region; `auto` for Cloudflare R2 |
| `bucket` | A bucket dedicated to this deployment, with credentials scoped to it |
| `recipient` | An age X25519 public key from the deployment's recovery key pair |
| `interval` | Minimum interval between automatic backups; default `24h` |
| `retention` | Archive age to retain; default `336h` (fourteen nights) |

Generate the key pair with `age-keygen`. The installer stores the private key in
the host's protected `/opt/ddp/.env` as `BACKUP_AGE_IDENTITY`, because the weekly
restore test needs it; only maintenance containers receive it. Also keep an offline
copy of the private key away from the host and the bucket, so the dealership can
restore if the host is lost. Supply `BACKUP_ACCESS_KEY_ID` and
`BACKUP_SECRET_ACCESS_KEY` to backup maintenance commands. Restore also requires
`BACKUP_AGE_IDENTITY`, containing the corresponding private key. The registry stores
only the public recipient. Retain old private keys until their archives expire;
restore one archive with the key that encrypted it.

The storage implementation uses the official AWS Go SDK with static credentials
and multipart transfers. It does not load local AWS profiles or instance identity.
HTTPS certificate validation and redirect refusal apply to storage. R2's region and
endpoint follow its [Go SDK example](https://developers.cloudflare.com/r2/examples/aws/aws-sdk-go/).
Encryption uses the [age format and library](https://pkg.go.dev/filippo.io/age).

## Run and inspect

The scheduler owns `ddp:backup`. When `deploy.backup` is configured, it checks
once a minute and creates an archive when the declared interval is due. The default
interval is 24 hours. Each attempt has a two-hour deadline and no scheduler retry;
a later tick can retry a failed backup. The shared database lock prevents overlap
with manual backup commands. Use `ddp jobs list`, `jobs pause`, `jobs resume`,
`jobs run ddp:backup`, and `runs cancel` to inspect and control it. Manual job runs
also respect the interval; `ddp backup run` creates an archive immediately.
Removing the configuration stops scheduling and preserves job history.

Supply the scheduler with `BACKUP_DATABASE_URL`, the two storage credentials, and
`DDP_IMAGE_DIGEST` from the running release's exact GHCR digest. The image embeds
its Git revision at build time. A development build or mutable image tag is refused.
The backup URL uses a LOGIN member of `ddp_backup`, separate from the scheduler's
normal database credential. It can read all data, including authentication records,
and write backup evidence and audit entries. It cannot change business data or
create databases, roles or schemas. API and Python job processes must not
receive this credential or the storage secrets. Python subprocesses receive only
their existing declared environment.

`ddp_backup` inherits PostgreSQL's
[`pg_read_all_data`](https://www.postgresql.org/docs/17/predefined-roles.html), which
covers future tables and schemas on this deployment's dedicated cluster. It does not
bypass row security; a dump that cannot read the complete database fails. Initial
bootstrap creates the role. Before upgrading an existing installation through
`ddp_owner`, a cluster administrator must provision `ddp_backup NOLOGIN` and grant
it `pg_read_all_data`. The migration then applies the narrow table and audit grants.
Provision a separate LOGIN member and password through host deployment; do not turn
the component group role into a LOGIN or grant it an owner/service role.

For `ddp backup run`, `DATABASE_URL` names the source database and can use the same
backup credential. Restore runs separately under host maintenance credentials that
can create a database and preserve archived owners and grants. Keep these credentials
and `BACKUP_AGE_IDENTITY` out of the scheduler, API and job environments.

Use the actual recorded revision and digest:

```sh
ddp backup run --revision "$RELEASE_REVISION" --image "$RELEASE_IMAGE_DIGEST" --json
ddp backup list --json
ddp backup run --revision "$RELEASE_REVISION" --image "$RELEASE_IMAGE_DIGEST" --if-due
ddp backup restore latest --verify --if-due --json
```

`--if-due` makes repeated maintenance invocations skip a recent successful backup
or restore check. Restore checks are due after seven days. Commands default to a
two-hour timeout; `--timeout` changes it for direct commands. The host timer below
performs weekly verification; the scheduler never performs a restore.

Retention runs after a new archive is verified. It keeps the new archive and the
last successfully restored archive even when the latter is older than retention.
Other expired manifests are removed before their archives. A crash can leave an
unlisted encrypted object; a later backup retries deletion once its ID is older
than retention. The `backups/<project>/` namespace is reserved for these archives.
Failed uploads never publish a manifest. Operational backup records,
restore receipts and audit history are retained.

## Enable weekly host verification

On a Linux host with the dealership's checkout at `/opt/ddp`, install the checked-in
systemd units after provisioning Compose, encrypted staging, storage credentials
and the recovery key. The procedure requires Bash, Docker Compose, jq and flock
(from util-linux), plus registry access to the retained release images.

```sh
cd /opt/ddp
install -m 0644 deploy/ddp-backup-verify.service deploy/ddp-backup-verify.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now ddp-backup-verify.timer
systemctl start ddp-backup-verify.service
journalctl -u ddp-backup-verify.service -e
systemctl list-timers ddp-backup-verify.timer
```

The timer runs Sundays at 04:00 UTC with up to thirty minutes of jitter and catches
missed runs after host downtime. Its service has a three-hour limit. A nonblocking
host lock prevents overlapping invocations. `bin/verify-backup.sh` can also run
manually from the checkout; `--if-due` skips a successful check less than seven days old.

The script reads manifests using the running API image, selects the newest archive,
and restores with that manifest's immutable image. It requires the same GHCR
repository as the API and checks the image's embedded revision before restoration.
This matters after a release adds migrations that were absent from the archive.
Retain those images alongside their backups. The script does not rewrite `.env` or
restart services. Cluster administration and the recovery key reach only maintenance
containers; private host files and disposable containers are removed on exit.
Failures exit nonzero and the restore command records failure evidence for health.

## Verify or recover

`--verify` creates a disposable database, restores the newest selected archive,
checks embedded migrations and all declared table/model contracts through a
read-only maintenance connection, and executes a
bounded list query for each declarative endpoint under `ddp_api`. Verification
connections enforce read-only transactions. No ingest, model apply, integration,
SMTP, login or scheduler work runs. This check does not replace browser, custom
route, authentication or export acceptance tests.

To retain a recovered database, run the image and registry recorded in the manifest:

```sh
ddp backup restore BACKUP_ID --database recovered_client --confirm recovered_client --json
```

`DATABASE_URL` selects the destination cluster. Recovery connects through its
`postgres` maintenance database, so the original database may be missing. Provision
the six DDP NOLOGIN component roles, including `ddp_backup`, first; restore preserves archived ownership
and grants and does not create cluster roles or reset passwords. Custom roles in
an archive must also exist. The target name must be new. Existing databases are
always refused, and a failed retained target remains for inspection. Connecting
the application to the recovered database is an explicit deployment step; follow
the [operator recovery procedure](runbook.md#recover-a-database-on-the-existing-host).

Restore requires PostgreSQL client tools compatible with the archive. The image,
CI and development container use version 17; local developers must install 17 or
newer. See [PostgreSQL restore options](https://www.postgresql.org/docs/17/app-pgrestore.html).
External database connections require `sslmode=verify-full`; the local Compose
Postgres route and loopback development databases may use local transport.

Every restore records an off-host intent before creating a database, then a
completion or failure receipt. It also updates the source ledger when available;
a retained restored database receives its own audit entry. A crash leaves the
running intent visible. `ddp:backup` reports current source-ledger evidence through
health and doctor; a new backup is insufficient until a restore check is current.
Missing, stale, failed or interrupted evidence cannot report healthy.

Plain dumps exist only in private temporary directories during maintenance. Set
`TMPDIR` to the encrypted backup staging volume on production hosts. The
[installer](deployment.md#fresh-host-installer) requires already-encrypted staging
and installs the verification timer; host-volume encryption, bucket provisioning
and custody of the recovery key remain the dealership's responsibility.
