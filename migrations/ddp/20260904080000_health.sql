CREATE TABLE ops.health_evaluations (
  id text PRIMARY KEY,
  rule_ref text NOT NULL,
  observed_at timestamptz NOT NULL,
  observation jsonb NOT NULL CHECK (jsonb_typeof(observation) = 'object'),
  state text NOT NULL CHECK (state IN ('ok', 'failing', 'unknown')),
  severity text NOT NULL CHECK (severity IN ('warning', 'critical'))
);
CREATE INDEX health_evaluations_recent ON ops.health_evaluations(rule_ref, observed_at DESC, id DESC);
CREATE TABLE ops.alert_state (
  rule_ref text PRIMARY KEY,
  state text NOT NULL CHECK (state IN ('ok', 'failing', 'unknown')),
  evaluation_id text NOT NULL REFERENCES ops.health_evaluations(id),
  incident_id text,
  last_alert_at timestamptz,
  recipients text[] NOT NULL DEFAULT '{}'
);
CREATE TABLE ops.alerts (
  id text PRIMARY KEY,
  rule_ref text NOT NULL,
  evaluation_id text NOT NULL REFERENCES ops.health_evaluations(id),
  incident_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('alert', 'recovery', 'reminder')),
  created_at timestamptz NOT NULL,
  recipients text[] NOT NULL CHECK (cardinality(recipients) > 0),
  context jsonb NOT NULL CHECK (jsonb_typeof(context) = 'object'),
  message_id text REFERENCES ops.outbox(id),
  notification_error text
);
CREATE INDEX alerts_pending ON ops.alerts(created_at, id) WHERE message_id IS NULL;
ALTER TABLE ops.health_evaluations OWNER TO ddp_owner;
ALTER TABLE ops.alert_state OWNER TO ddp_owner;
ALTER TABLE ops.alerts OWNER TO ddp_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON ops.health_evaluations, ops.alert_state, ops.alerts TO ddp_scheduler;
GRANT SELECT ON ops.health_evaluations, ops.alert_state, ops.alerts TO ddp_api, ddp_readonly;
