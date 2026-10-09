ALTER TABLE ops.executions DROP CONSTRAINT executions_status_check;
ALTER TABLE ops.executions ADD CONSTRAINT executions_status_check
  CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'interrupted'));
ALTER TABLE ops.executions ALTER COLUMN started_at DROP NOT NULL;
ALTER TABLE ops.executions ALTER COLUMN started_at DROP DEFAULT;
ALTER TABLE ops.executions ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE ops.executions ADD COLUMN available_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE ops.executions ADD COLUMN max_attempts integer NOT NULL DEFAULT 1 CHECK (max_attempts > 0);
ALTER TABLE ops.executions ADD COLUMN retry_initial_ns bigint NOT NULL DEFAULT 1000000000 CHECK (retry_initial_ns > 0);
ALTER TABLE ops.executions ADD COLUMN retry_max_ns bigint NOT NULL DEFAULT 1000000000 CHECK (retry_max_ns >= retry_initial_ns);
UPDATE ops.executions SET created_at=started_at, available_at=scheduled_at;
CREATE INDEX executions_due ON ops.executions(available_at, id) WHERE status='queued';
ALTER TABLE ops.attempts ADD COLUMN number integer;
WITH numbered AS (
  SELECT id, row_number() OVER (PARTITION BY execution_id ORDER BY started_at, id) AS number
  FROM ops.attempts
)
UPDATE ops.attempts a SET number=n.number FROM numbered n WHERE n.id=a.id;
ALTER TABLE ops.attempts ALTER COLUMN number SET NOT NULL;
ALTER TABLE ops.attempts ADD CONSTRAINT attempts_number_positive CHECK (number > 0);
ALTER TABLE ops.attempts ADD CONSTRAINT attempts_execution_number UNIQUE (execution_id, number);
CREATE UNIQUE INDEX attempts_one_running ON ops.attempts(execution_id) WHERE status='running';
