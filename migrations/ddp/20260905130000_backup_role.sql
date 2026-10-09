-- A separate credential can dump all data without owning or changing client data.
-- Bootstrap needs cluster administration. On an existing installation, provision
-- this role and membership before applying this migration as ddp_owner.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ddp_backup') THEN
    BEGIN
      CREATE ROLE ddp_backup NOLOGIN;
    EXCEPTION WHEN duplicate_object OR unique_violation THEN
      IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'ddp_backup') THEN
        RAISE;
      END IF;
    END;
  END IF;
  IF NOT pg_has_role('ddp_backup', 'pg_read_all_data', 'MEMBER') THEN
    GRANT pg_read_all_data TO ddp_backup;
  END IF;
END
$$;

GRANT INSERT (id, status, deadline_at) ON ops.backups TO ddp_backup;
GRANT UPDATE (status, finished_at, manifest, error) ON ops.backups TO ddp_backup;
GRANT INSERT (id, action, target, outcome) ON ddp.audit TO ddp_backup;
