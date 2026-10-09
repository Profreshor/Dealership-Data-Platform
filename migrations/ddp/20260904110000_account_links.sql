ALTER TABLE app.users ALTER COLUMN password_hash DROP NOT NULL;
CREATE TABLE app.password_tokens (
  token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
  user_id text NOT NULL UNIQUE REFERENCES app.users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('invite','reset')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '1 hour',
  CHECK (expires_at > created_at)
);
CREATE INDEX password_tokens_expiry ON app.password_tokens(expires_at);
ALTER TABLE app.password_tokens OWNER TO ddp_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.password_tokens TO ddp_api;

CREATE FUNCTION ddp.expire_password_tokens() RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE removed integer;
BEGIN
  DELETE FROM app.password_tokens WHERE token_hash IN (
    SELECT token_hash FROM app.password_tokens WHERE expires_at <= clock_timestamp()
    ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED
  );
  GET DIAGNOSTICS removed = ROW_COUNT;
  RETURN removed;
END
$$;
ALTER FUNCTION ddp.expire_password_tokens() OWNER TO ddp_owner;
REVOKE ALL ON FUNCTION ddp.expire_password_tokens() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ddp.expire_password_tokens() TO ddp_scheduler;
