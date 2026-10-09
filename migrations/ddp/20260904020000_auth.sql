CREATE TABLE app.users (
  id text PRIMARY KEY,
  email text NOT NULL UNIQUE CHECK (email = lower(email)),
  password_hash text NOT NULL,
  is_admin boolean NOT NULL DEFAULT false,
  disabled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE app.roles (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE
);
CREATE TABLE app.permissions (name text PRIMARY KEY);
CREATE TABLE app.user_roles (
  user_id text NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
  role_id text NOT NULL REFERENCES app.roles(id) ON DELETE CASCADE,
  PRIMARY KEY(user_id, role_id)
);
CREATE TABLE app.role_permissions (
  role_id text NOT NULL REFERENCES app.roles(id) ON DELETE CASCADE,
  permission text NOT NULL REFERENCES app.permissions(name) ON DELETE CASCADE,
  PRIMARY KEY(role_id, permission)
);
CREATE TABLE app.sessions (
  token_hash bytea PRIMARY KEY,
  user_id text NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user ON app.sessions(user_id);
CREATE INDEX sessions_expiry ON app.sessions(expires_at);
ALTER TABLE app.users OWNER TO ddp_owner;
ALTER TABLE app.roles OWNER TO ddp_owner;
ALTER TABLE app.permissions OWNER TO ddp_owner;
ALTER TABLE app.user_roles OWNER TO ddp_owner;
ALTER TABLE app.role_permissions OWNER TO ddp_owner;
ALTER TABLE app.sessions OWNER TO ddp_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.users, app.roles, app.permissions, app.user_roles, app.role_permissions, app.sessions TO ddp_api;
