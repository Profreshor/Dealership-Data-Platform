ALTER TABLE ops.executions DROP CONSTRAINT executions_status_check;
ALTER TABLE ops.executions ADD CONSTRAINT executions_status_check
  CHECK (status IN ('waiting','queued','running','succeeded','failed','interrupted','skipped'));
ALTER TABLE ops.executions ADD COLUMN dispatch text NOT NULL DEFAULT 'manual' CHECK (dispatch IN ('manual','scheduler'));
ALTER TABLE ops.executions ADD COLUMN chain_id text REFERENCES ops.executions(id);
ALTER TABLE ops.executions ADD COLUMN depends_on text[] NOT NULL DEFAULT '{}';
ALTER TABLE ops.executions ADD COLUMN reason text;
CREATE INDEX executions_chain ON ops.executions(chain_id);
CREATE TABLE ops.ticks (
  id text PRIMARY KEY,
  job_ref text NOT NULL,
  scheduled_at timestamptz NOT NULL,
  local_time text NOT NULL,
  timezone text NOT NULL,
  status text NOT NULL CHECK (status IN ('launched','skipped')),
  execution_id text REFERENCES ops.executions(id),
  reason text,
  UNIQUE(job_ref,timezone,local_time)
);
CREATE TABLE ops.planner_state (
  job_ref text PRIMARY KEY,
  planned_through timestamptz NOT NULL
);
CREATE TABLE ops.job_overrides (
  job_ref text PRIMARY KEY,
  paused boolean NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ops.heartbeats (
  service_ref text PRIMARY KEY,
  instance_id text NOT NULL,
  seen_at timestamptz NOT NULL,
  state text NOT NULL
);
CREATE TABLE ops.events (
  id text PRIMARY KEY,
  resource_ref text NOT NULL,
  kind text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  message text NOT NULL
);
ALTER TABLE ops.ticks OWNER TO ddp_owner;
ALTER TABLE ops.planner_state OWNER TO ddp_owner;
ALTER TABLE ops.job_overrides OWNER TO ddp_owner;
ALTER TABLE ops.heartbeats OWNER TO ddp_owner;
ALTER TABLE ops.events OWNER TO ddp_owner;
GRANT SELECT,INSERT,UPDATE,DELETE ON ops.ticks,ops.planner_state,ops.job_overrides,ops.heartbeats,ops.events TO ddp_scheduler;
GRANT SELECT ON ops.ticks,ops.planner_state,ops.job_overrides,ops.heartbeats,ops.events TO ddp_api,ddp_readonly;
