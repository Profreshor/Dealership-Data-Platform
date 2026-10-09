ALTER TABLE ops.executions ADD COLUMN cancel_requested_at timestamptz;
CREATE TABLE ddp.audit (
  id text PRIMARY KEY,
  principal text NOT NULL DEFAULT session_user,
  action text NOT NULL,
  target text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  outcome jsonb NOT NULL
);
ALTER TABLE ddp.audit OWNER TO ddp_owner;
GRANT INSERT ON ddp.audit TO ddp_scheduler;
GRANT SELECT ON ddp.audit TO ddp_api,ddp_readonly;
