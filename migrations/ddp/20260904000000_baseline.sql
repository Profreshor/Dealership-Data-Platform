-- Platform schemas and component roles. Object-specific grants live here so
-- later migrations can declare grants alongside the objects they introduce.
CREATE SCHEMA IF NOT EXISTS ops;
CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS staging;
CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS mart;

-- Roles are cluster-wide; independent disposable databases can bootstrap together.
DO $$
DECLARE component text;
BEGIN
  FOREACH component IN ARRAY ARRAY['ddp_owner', 'ddp_scheduler', 'ddp_job', 'ddp_api', 'ddp_readonly'] LOOP
    BEGIN
      EXECUTE format('CREATE ROLE %I NOLOGIN', component);
    EXCEPTION WHEN duplicate_object OR unique_violation THEN
      IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = component) THEN
        RAISE;
      END IF;
    END;
  END LOOP;
END
$$;

ALTER SCHEMA ddp OWNER TO ddp_owner;
ALTER SCHEMA ops OWNER TO ddp_owner;
ALTER SCHEMA app OWNER TO ddp_owner;
ALTER SCHEMA staging OWNER TO ddp_owner;
ALTER SCHEMA core OWNER TO ddp_owner;
ALTER SCHEMA mart OWNER TO ddp_owner;

REVOKE ALL ON SCHEMA ddp, ops, app, staging, core, mart FROM PUBLIC;
GRANT USAGE ON SCHEMA ops TO ddp_scheduler, ddp_api, ddp_readonly;
GRANT USAGE ON SCHEMA app TO ddp_api;
GRANT USAGE ON SCHEMA staging, core, mart TO ddp_job, ddp_api, ddp_readonly;

ALTER TABLE ddp.platform_migrations OWNER TO ddp_owner;
ALTER TABLE ddp.client_migrations OWNER TO ddp_owner;
GRANT USAGE ON SCHEMA ddp TO ddp_scheduler, ddp_job, ddp_api, ddp_readonly;
GRANT SELECT ON ddp.platform_migrations, ddp.client_migrations TO ddp_scheduler, ddp_job, ddp_api, ddp_readonly;
