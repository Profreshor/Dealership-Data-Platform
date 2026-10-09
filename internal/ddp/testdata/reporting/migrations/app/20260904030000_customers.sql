CREATE SCHEMA synthetic AUTHORIZATION ddp_owner;
CREATE TABLE synthetic.customers (
  id text PRIMARY KEY,
  payload jsonb NOT NULL,
  _loaded_at timestamptz NOT NULL,
  _source_key text NOT NULL
);
ALTER TABLE synthetic.customers OWNER TO ddp_owner;
GRANT USAGE ON SCHEMA synthetic TO ddp_job, ddp_scheduler, ddp_readonly;
GRANT SELECT, INSERT, UPDATE, DELETE ON synthetic.customers TO ddp_job;
GRANT SELECT ON synthetic.customers TO ddp_scheduler, ddp_readonly;
