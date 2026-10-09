CREATE TABLE ops.executions (
  id text PRIMARY KEY,
  job_ref text NOT NULL,
  scheduled_at timestamptz NOT NULL,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted'))
);
CREATE TABLE ops.attempts (
  id text PRIMARY KEY,
  execution_id text NOT NULL REFERENCES ops.executions(id),
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted')),
  stdout text NOT NULL DEFAULT '',
  stderr text NOT NULL DEFAULT '',
  result jsonb,
  error text
);
CREATE INDEX attempts_execution ON ops.attempts(execution_id);
CREATE TABLE ops.watermarks (
  job_ref text PRIMARY KEY,
  value jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ops.model_refreshes (
  id text PRIMARY KEY,
  model_ref text NOT NULL,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  error text
);
ALTER TABLE ops.executions OWNER TO ddp_owner;
ALTER TABLE ops.attempts OWNER TO ddp_owner;
ALTER TABLE ops.watermarks OWNER TO ddp_owner;
ALTER TABLE ops.model_refreshes OWNER TO ddp_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON ops.executions, ops.attempts, ops.watermarks, ops.model_refreshes TO ddp_scheduler;
GRANT SELECT ON ops.executions, ops.attempts, ops.watermarks, ops.model_refreshes TO ddp_api, ddp_readonly;
GRANT USAGE, CREATE ON SCHEMA staging, core, mart TO ddp_scheduler;
