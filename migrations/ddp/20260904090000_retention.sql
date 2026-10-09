ALTER TABLE ops.attempts ADD COLUMN payload_expired_at timestamptz;
CREATE INDEX attempts_retention ON ops.attempts(finished_at,id)
  WHERE payload_expired_at IS NULL AND finished_at IS NOT NULL AND status <> 'running';
ALTER TABLE ops.outbox ADD COLUMN content_expired_at timestamptz;
ALTER TABLE ops.outbox DROP CONSTRAINT outbox_check;
ALTER TABLE ops.outbox ADD CONSTRAINT outbox_rendered_content
  CHECK (content_expired_at IS NOT NULL OR rendered_at IS NULL OR
    (subject IS NOT NULL AND text_body IS NOT NULL AND html_body IS NOT NULL));
ALTER TABLE ops.outbox ADD CONSTRAINT outbox_expired_content
  CHECK (content_expired_at IS NULL OR
    (status IN ('delivered','failed') AND context='{}'::jsonb AND
      subject IS NULL AND text_body IS NULL AND html_body IS NULL));
CREATE INDEX outbox_retention ON ops.outbox(finished_at,id)
  WHERE content_expired_at IS NULL AND finished_at IS NOT NULL AND status IN ('delivered','failed');
GRANT SELECT(content_expired_at) ON ops.outbox TO ddp_api,ddp_readonly;
ALTER TABLE ops.health_evaluations ADD COLUMN evidence_expired_at timestamptz;
CREATE INDEX health_evaluations_retention ON ops.health_evaluations(observed_at,id)
  WHERE evidence_expired_at IS NULL;

-- A scheduler can remove expired sessions without reading authentication data.
CREATE FUNCTION ddp.expire_sessions() RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE removed integer;
BEGIN
  DELETE FROM app.sessions WHERE token_hash IN (
    SELECT token_hash FROM app.sessions WHERE expires_at <= clock_timestamp()
    ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED
  );
  GET DIAGNOSTICS removed = ROW_COUNT;
  RETURN removed;
END
$$;
ALTER FUNCTION ddp.expire_sessions() OWNER TO ddp_owner;
REVOKE ALL ON FUNCTION ddp.expire_sessions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ddp.expire_sessions() TO ddp_scheduler;
