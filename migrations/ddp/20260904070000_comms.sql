CREATE TABLE ops.outbox (
  id text PRIMARY KEY,
  effect_key text NOT NULL UNIQUE CHECK (length(effect_key) BETWEEN 1 AND 256),
  payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
  template text NOT NULL CHECK (template ~ '^[a-z][a-z0-9_-]{0,63}$'),
  context jsonb NOT NULL CHECK (jsonb_typeof(context) = 'object' AND octet_length(context::text) <= 1048576),
  sender text NOT NULL,
  recipients text[] NOT NULL CHECK (cardinality(recipients) BETWEEN 1 AND 100),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivering', 'delivered', 'failed')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  rendered_at timestamptz,
  subject text,
  text_body text,
  html_body text,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
  finished_at timestamptz,
  last_error text,
  CHECK (rendered_at IS NULL OR (subject IS NOT NULL AND text_body IS NOT NULL AND html_body IS NOT NULL))
);
CREATE INDEX outbox_due ON ops.outbox(available_at, id) WHERE status = 'pending';
CREATE TABLE ops.deliveries (
  id text PRIMARY KEY,
  message_id text NOT NULL REFERENCES ops.outbox(id),
  number integer NOT NULL CHECK (number > 0),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  finished_at timestamptz,
  status text NOT NULL CHECK (status IN ('delivering', 'delivered', 'failed', 'interrupted')),
  error text,
  UNIQUE(message_id, number)
);
ALTER TABLE ops.outbox OWNER TO ddp_owner;
ALTER TABLE ops.deliveries OWNER TO ddp_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON ops.outbox, ops.deliveries TO ddp_scheduler;
GRANT USAGE ON SCHEMA ops TO ddp_job;
GRANT INSERT (id, effect_key, payload_hash, template, context, sender, recipients) ON ops.outbox TO ddp_job, ddp_api;
GRANT SELECT (id, effect_key, payload_hash) ON ops.outbox TO ddp_job;
GRANT SELECT (id, effect_key, payload_hash, template, status, created_at, available_at, rendered_at, attempts, max_attempts, finished_at, last_error) ON ops.outbox TO ddp_api;
GRANT SELECT (id, effect_key, template, status, created_at, available_at, rendered_at, attempts, max_attempts, finished_at, last_error) ON ops.outbox TO ddp_readonly;
GRANT SELECT ON ops.deliveries TO ddp_api, ddp_readonly;
