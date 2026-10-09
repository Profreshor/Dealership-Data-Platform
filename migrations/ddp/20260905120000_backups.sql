-- Host maintenance commands own backup writes; services only inspect their evidence.
CREATE TABLE ops.backups (
  id text PRIMARY KEY,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  deadline_at timestamptz NOT NULL,
  finished_at timestamptz,
  manifest jsonb,
  error text
);
CREATE TABLE ops.backup_restores (
  id text PRIMARY KEY,
  backup_id text NOT NULL,
  target_database text NOT NULL,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  deadline_at timestamptz NOT NULL,
  finished_at timestamptz,
  error text
);
ALTER TABLE ops.backups OWNER TO ddp_owner;
ALTER TABLE ops.backup_restores OWNER TO ddp_owner;
GRANT SELECT ON ops.backups, ops.backup_restores TO ddp_api, ddp_scheduler, ddp_readonly;
