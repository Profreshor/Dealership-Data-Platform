-- Only host maintenance records release outcomes; services observe and alert.
CREATE TABLE ops.deployments (
  id text PRIMARY KEY,
  image text NOT NULL,
  previous_image text NOT NULL,
  revision text NOT NULL,
  status text NOT NULL CHECK (status IN ('succeeded', 'failed')),
  phase text NOT NULL CHECK (phase IN ('preflight', 'backup', 'migrations', 'models', 'startup', 'rollback', 'ready')),
  finished_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK ((status = 'succeeded') = (phase = 'ready'))
);
ALTER TABLE ops.deployments OWNER TO ddp_owner;
GRANT SELECT ON ops.deployments TO ddp_api, ddp_scheduler, ddp_readonly;
