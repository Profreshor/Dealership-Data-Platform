# Operator runbook

This runbook is for the operator: the person who runs the system for the
dealership. That is normally someone at the dealership; it can be an IT provider the
dealership hires, with access the dealership grants and can revoke. Use the dealership's completed
discovery and `ddp.yaml` for contacts, integrations, reporting expectations and
recovery targets. Record the incident, selected image, backup, commands and results
in the dealership's protected operations record. Keep credentials and customer rows
out of tickets and development fixtures.

## Install and release

Follow [deployment](deployment.md) for host inputs, component credentials, initial
migration, Compose startup and the updater. Follow [accounts](accounts.md) for
operator bootstrap. Confirm the portal through its approved domain and the
administrator's SSH sign-in through the dealership's Cloudflare Access policy with MFA.
Keep console access available. The installer does not configure Cloudflare; a
running host alone does not prove the routes and Access policy work.

A reviewed merge publishes the image. Inspect `ddp deploy status --json`,
`ddp health --json` and the updater journal after activation. The updater restores
the previous image when startup fails and preserves applied migrations. Publish a
corrected release to replace a rejected digest; see [release behavior](deployment.md#enable-automatic-releases).

## Investigate

Start with `ddp status --json`, `ddp doctor --json` and `ddp health --json`.
Use the returned resource references with `ddp inspect`, `ddp diagnose` and
`ddp logs`. The [TUI](tui.md) and [System console](system-console.md) expose the
same operational facts.

On the production host, investigate with the read-only database login: follow the
[investigation procedure](../skills/ddp-investigate/SKILL.md). It runs the
image's `ddp` commands through `docker compose run maintenance` with
`ddp_readonly_login`, whose PostgreSQL grants allow only reads, and reads the
updater, backup-verification and installer doctor evidence on the host. Sign in
with the dealership's administrator account at the console or through the SSH
route protected by the dealership's own Cloudflare Access policy.

Before retrying a failed job, inspect its last attempt, dependencies, write mode and
watermark. Confirm the source has recovered and replay is safe. Before resending an
email, inspect persisted delivery evidence: a deliberate resend is a new action.
Use CLI help and [command documentation](commands.md) for confirmation requirements.

## Verify backups

Follow [backup setup](backups.md) for the off-host bucket, encryption and separate
key custody. Inspect `ddp backup list --json` and the weekly verification journal.
A new archive does not replace a successful restore check. Retain the immutable
image and recovery key named by each retained archive.

## Recover a database on the existing host

This procedure retains the original database and uses the current application
image. Rehearse it on a disposable deployment before production use. A replacement host
first needs the deployment’s protected configuration, retained images and six
component roles/logins. The supported Compose database name is `ddp`.

1. Decide the recovery point and the acceptable service interruption. Confirm disk
   capacity for both databases. Inspect `.ddp-release.json` and the API container image; resolve
   any pending updater recovery first.
   Select the backup ID, image and revision from `backup list --json`. Verify the
   archive image belongs to this deployment's repository and its `version --json`
   matches the manifest. [Weekly verification](backups.md#enable-weekly-host-verification)
   performs these checks for the newest archive while the API is running.
2. In a root Bash session at `/opt/ddp`, stop the timers and acquire their locks.
   Let running maintenance finish before acquiring them. Keep cloudflared running
   to preserve the remote connection.

   ```sh
   set -euo pipefail
   umask 077
   compose() { docker compose --env-file .env -f deploy/compose.yaml "$@"; }
   systemctl stop ddp-update.timer ddp-backup-verify.timer
   exec 9>.ddp-update.lock
   flock 9
   exec 10>.ddp-backup-verify.lock
   flock 10
   compose stop scheduler api
   ```

3. Read back the image and release state after acquiring the locks; an updater
   may have finished while you waited. Save the protected inputs and derive the
   maintenance URLs without printing them:

   ```sh
   recovery_dir=$(mktemp -d /root/ddp-recovery.XXXXXX)
   cp .env .ddp-release.json "$recovery_dir/"
   compose --profile maintenance config --format json > "$recovery_dir/compose.json"
   current_image=$(jq -er '.services.api.image' "$recovery_dir/compose.json")
   test "$(docker inspect --format '{{.Config.Image}}' "$(compose ps -a -q api)")" = "$current_image"
   jq -e --arg image "$current_image" \
     '.version == 1 and .current == $image and .pending == ""' .ddp-release.json >/dev/null
   admin_url=$(jq -er '"postgres://postgres:" + (.services.postgres.environment.POSTGRES_PASSWORD | @uri) + "@postgres:5432/ddp?sslmode=disable"' "$recovery_dir/compose.json")
   owner_url=$(jq -er '.services.maintenance.environment.DATABASE_URL | sub("/ddp\\?"; "/recovered_client?")' "$recovery_dir/compose.json")
   scheduler_url=$(jq -er '.services.scheduler.environment.DATABASE_URL | sub("/ddp\\?"; "/recovered_client?")' "$recovery_dir/compose.json")
   ```

   Use the Postgres administrator only for restore and database renaming. Restore
   the selected `backup_id` with its recorded `archive_image` into the new name
   `recovered_client`, as documented under [retained restore](backups.md#verify-or-recover).
   Stop if that name already exists. Require the current and archived image
   references to use the same repository and immutable SHA-256 digests.

   ```sh
   DDP_IMAGE_DIGEST="$archive_image" DATABASE_URL="$admin_url" \
     compose run --rm --no-deps -e DATABASE_URL maintenance \
     backup restore "$backup_id" --database recovered_client \
     --confirm recovered_client --json
   ```

4. Advance the candidate using the current image and its component roles:

   ```sh
   DDP_IMAGE_DIGEST="$current_image" DATABASE_URL="$owner_url" \
     compose run --rm --no-deps -e DATABASE_URL maintenance migrate up --json
   DDP_IMAGE_DIGEST="$current_image" DATABASE_URL="$scheduler_url" \
     compose run --rm --no-deps -e DATABASE_URL maintenance models apply --json
   DDP_IMAGE_DIGEST="$current_image" DATABASE_URL="$scheduler_url" \
     compose run --rm --no-deps -e DATABASE_URL maintenance models verify --json
   ```

   Require successful JSON envelopes from all three commands. Check recovered rows and permissions
   against the chosen recovery point. Keep the scheduler stopped: restored queued
   messages and jobs may repeat effects that already occurred outside Postgres.
   Reconcile those effects and access changes before enabling processing.
5. From Postgres’s maintenance database, block new connections and terminate the
   remaining connections to both databases. Ensure `ddp_pre_recovery` is unused.
   Run each block with `psql -X -v ON_ERROR_STOP=1 -U postgres -d postgres` through
   `compose exec -T postgres`. A failed command stops the procedure.

   ```sql
   ALTER DATABASE ddp ALLOW_CONNECTIONS false;
   ALTER DATABASE recovered_client ALLOW_CONNECTIONS false;
   SELECT pg_terminate_backend(pid) FROM pg_stat_activity
   WHERE datname IN ('ddp', 'recovered_client');
   ```

   Perform the rename in one transaction:

   ```sql
   BEGIN;
   ALTER DATABASE ddp RENAME TO ddp_pre_recovery;
   ALTER DATABASE recovered_client RENAME TO ddp;
   ALTER DATABASE ddp ALLOW_CONNECTIONS true;
   COMMIT;
   ```

6. Keep `.env` and `.ddp-release.json` unchanged. Start the current API with
   `compose up -d --no-deps --wait --wait-timeout 120 api`. Check `/readyz`, log in
   through the portal, and verify the expected reports and access rules. After effect
   reconciliation, start the scheduler with the same Compose options. Check health
   and record the completed recovery. Deployment history inside the archive is a
   historical snapshot; compare it with the saved host state and incident record.
   After the checks pass, record this activation using the current image's
   `version --json` revision. Use the live database's owner connection:

   ```sh
   DDP_IMAGE_DIGEST="$current_image" compose run --rm --no-deps maintenance version --json
   DDP_IMAGE_DIGEST="$current_image" compose run --rm --no-deps maintenance deploy record \
     --current "$current_image" --image "$current_image" --revision "$current_revision" \
     --status succeeded --phase ready --json
   ```

   The image is unchanged, so both image arguments identify that same digest.
   The new outcome restores current deployment evidence in the recovered database;
   retain the backup ID and database swap details in the recovery record.
7. Release both locks with `flock -u 9` and `flock -u 10`, then restart the two timers.
   Make and verify a fresh backup. Keep the original database inaccessible until
   the dealership's retention period ends; its deletion is a separate maintenance action.

PostgreSQL allows [database renaming](https://www.postgresql.org/docs/17/sql-alterdatabase.html)
from another database. Blocking connections prevents a reconnect race during the
swap. An SQL failure rolls back both renames; inspect actual names before retrying.

## If recovery fails

Before the rename, the original database is intact. Re-enable its connections if
blocked, then restart its current API and scheduler when appropriate. Preserve the
failed candidate for inspection.

After the rename, stop the API and scheduler. Block connections to `ddp`, terminate
its sessions, and transactionally rename it to an unused `ddp_recovery_attempt`.
Rename `ddp_pre_recovery` back to `ddp` and set `ALLOW_CONNECTIONS true` before
committing. Start and check the original services. This restores the original
Postgres state; reconcile any external effects emitted after the attempted cutover.
Keep the timers stopped until the host and database state are understood.

## Take back control or decommission

Record deployment ownership, secret custody, backup and restore evidence, approved
access, schedules and open operational issues in the dealership's operations
record. When the dealership takes back control from an IT provider it hired, or
retires the deployment, follow the
[offboarding checklist](../skills/ddp-offboard/SKILL.md) for the fresh or final
backup, credential rotation, access removal and deletion after the dealership's
retention period.
