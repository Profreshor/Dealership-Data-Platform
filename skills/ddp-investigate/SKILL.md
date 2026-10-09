---
name: ddp-investigate
description: Investigate a production DDP failure with the local ddp CLI's read-only commands and the readonly database login, and return a factual report.
---

Use this skill for production inspection. Read [diagnosis](../../docs/diagnosis.md), [executions](../../docs/executions.md), and [commands](../../docs/commands.md).

1. Work only through access the dealership has already granted: its administrator at the host console, or over the SSH route protected by the dealership's own Cloudflare Access policy. Do not create accounts, keys, credentials or new access paths for the investigation.
2. On the host, run the image's `ddp` binary with the `ddp_readonly_login` database login, so PostgreSQL itself refuses database writes. The helper runs with `sudo`, so it reads the readonly password from `.env` without printing it, passes the URL by variable name so the password does not appear in the process list, and blanks the backup credentials the `maintenance` service would otherwise receive:

   ```bash
   cd /opt/ddp
   ddp_ro() {
     sudo sh -c 'pw=$(sed -n "s/^READONLY_DATABASE_PASSWORD=//p" .env)
       export DATABASE_URL="postgres://ddp_readonly_login:$pw@postgres:5432/ddp?sslmode=disable"
       exec docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps -e DATABASE_URL \
         -e BACKUP_ACCESS_KEY_ID= -e BACKUP_SECRET_ACCESS_KEY= -e BACKUP_AGE_IDENTITY= \
         maintenance "$@" --json' ddp_ro "$@"
   }
   ddp_ro status
   ddp_ro diagnose job/sync_customers
   ddp_ro logs job/sync_customers
   ```

3. Prefer the bounded inspection commands: `status`, `diagnose <ref>`, `inspect <ref>`, `health list|show|alerts`, `scheduler status`, `jobs list|show`, `runs list|show`, `logs <ref>`, `comms list|show`, `tables list|show`, `integrations list|show`, `migrate status`, `models plan|verify` and `deploy status`. Registry reads (`registry`, `search`, `routes`, `config validate`, `validate`, `version`) need no database. Use `ddp_ro sql "SELECT ..."` only when an inspection command does not expose the needed fact; it runs in a read-only transaction within the documented result bounds. Never add `--write`.
4. Host evidence may also come from `sudo docker compose --env-file .env -f deploy/compose.yaml ps`, `sudo journalctl -u ddp-update.service -n 100 --no-pager`, `sudo journalctl -u ddp-backup-verify.service -n 100 --no-pager` and `/var/lib/ddp/install-state/doctor.json`. Do not read or print `.env` values.
5. Record observed timestamps, command results, affected references, failure evidence, uncertainty, and the next human action. Separate facts from hypotheses.
6. Return the report without changing schedules, jobs, migrations, communications, deployments, backups, files, or database state.

Only the database side is enforced: `ddp_readonly` authority comes from PostgreSQL grants. These commands run with `sudo` on the host, so nothing technical stops a change to files, containers or services; staying read-only everywhere else depends on following this skill. A refusal or unavailable database is evidence to report, not a reason to seek another credential or bypass.
