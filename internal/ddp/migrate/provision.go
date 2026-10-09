package migrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var provisionPassword = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ProvisionResult struct {
	Login string `json:"login"`
	Group string `json:"group"`
}

// Provision creates or rotates one production login after the initial migrations.
// Passwords are 32 random bytes in hexadecimal, safe for Compose connection URLs.
func Provision(ctx context.Context, rawURL, component, password string) (ProvisionResult, error) {
	result := ProvisionResult{}
	if !slices.Contains([]string{"owner", "scheduler", "job", "api", "backup", "readonly"}, component) {
		return result, errors.New("component must be owner, scheduler, job, api, backup or readonly")
	}
	if !provisionPassword.MatchString(password) {
		return result, fmt.Errorf("%s_DATABASE_PASSWORD must contain exactly 64 lowercase hexadecimal characters; generate it with openssl rand -hex 32", strings.ToUpper(component))
	}
	if rawURL == "" {
		return result, errors.New("DATABASE_URL is required")
	}
	conn, err := pgx.Connect(ctx, rawURL)
	if err != nil {
		return result, errors.New("connect to provisioning database; check DATABASE_URL and administrator credentials")
	}
	defer conn.Close(context.Background())
	result.Group = "ddp_" + component
	result.Login = result.Group + "_login"
	var admin bool
	if err := conn.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname=session_user`).Scan(&admin); err != nil {
		return ProvisionResult{}, errors.New("check provisioning administrator failed")
	}
	if !admin {
		return ProvisionResult{}, fmt.Errorf("%w: provisioning requires the cluster administrator", audit.ErrRefused)
	}
	// Disable every native statement logging path before BEGIN: transaction
	// sampling is decided when the transaction starts, not when a password changes.
	if _, err := conn.Exec(ctx, `SET log_statement='none'; SET log_min_error_statement='panic';
SET log_min_duration_statement=-1; SET log_min_duration_sample=-1;
SET log_transaction_sample_rate=0; SET log_duration=off; SET password_encryption='scram-sha-256'`); err != nil {
		return ProvisionResult{}, errors.New("disable provisioning statement logging failed")
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// Serialize credential changes, including concurrent installer invocations.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1245793106, 1)`); err != nil {
			return err
		}
		var ready bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('ddp.audit') IS NOT NULL`).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("%w: apply initial migrations as administrator before provisioning", audit.ErrRefused)
		}
		if err := provisionLogin(ctx, tx, result, password); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "database.provision", "database_role/"+result.Login, map[string]string{"group": result.Group, "status": "provisioned"})
	})
	if err != nil {
		if errors.Is(err, audit.ErrRefused) {
			return ProvisionResult{}, err
		}
		if ctx.Err() != nil {
			return ProvisionResult{}, ctx.Err()
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return ProvisionResult{}, fmt.Errorf("provision database error: postgres %s", pe.Code)
		}
		return ProvisionResult{}, errors.New("provision database login failed")
	}
	return result, nil
}

func provisionLogin(ctx context.Context, tx pgx.Tx, result ProvisionResult, password string) error {
	var safe bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
SELECT FROM pg_roles WHERE rolname=$1 AND NOT rolcanlogin AND NOT rolsuper
AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls AND rolinherit
)`, result.Group).Scan(&safe); err != nil {
		return err
	}
	if !safe {
		return fmt.Errorf("%w: %s must be an existing restricted NOLOGIN group", audit.ErrRefused, result.Group)
	}
	// Only the archive group inherits another role, PostgreSQL's read-all-data role.
	var parents []string
	var optionsSafe bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(p.rolname ORDER BY p.rolname),'{}'), coalesce(bool_and(NOT m.admin_option AND m.inherit_option AND m.set_option),true)
FROM pg_auth_members m JOIN pg_roles p ON p.oid=m.roleid JOIN pg_roles r ON r.oid=m.member
WHERE r.rolname=$1`, result.Group).Scan(&parents, &optionsSafe); err != nil {
		return err
	}
	if !optionsSafe || (result.Group == "ddp_backup" && !slices.Equal(parents, []string{"pg_read_all_data"})) || (result.Group != "ddp_backup" && len(parents) != 0) {
		return fmt.Errorf("%w: unexpected memberships on %s", audit.ErrRefused, result.Group)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_roles WHERE rolname=$1)`, result.Login).Scan(&exists); err != nil {
		return err
	}
	login := pgx.Identifier{result.Login}.Sanitize()
	group := pgx.Identifier{result.Group}.Sanitize()
	if exists {
		if err := tx.QueryRow(ctx, `SELECT rolcanlogin AND rolinherit AND NOT rolsuper AND NOT rolcreatedb
AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls
AND (SELECT count(*)=1 AND bool_and(p.rolname=$2 AND NOT m.admin_option AND m.inherit_option AND m.set_option)
FROM pg_auth_members m JOIN pg_roles p ON p.oid=m.roleid WHERE m.member=r.oid)
FROM pg_roles r WHERE rolname=$1`, result.Login, result.Group).Scan(&safe); err != nil {
			return err
		}
		if !safe {
			return fmt.Errorf("%w: unexpected privileges on %s; inspect before rotating", audit.ErrRefused, result.Login)
		}
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (
SELECT FROM pg_db_role_setting s JOIN pg_roles r ON r.oid=s.setrole WHERE r.rolname=$1
AND NOT (s.setdatabase=0 AND s.setconfig=ARRAY['role=' || $2::text]))
AND NOT EXISTS (SELECT FROM pg_shdepend d JOIN pg_roles r ON r.oid=d.refobjid
WHERE d.refclassid='pg_authid'::regclass AND r.rolname=$1 AND d.deptype IN ('a','o'))`, result.Login, result.Group).Scan(&safe); err != nil {
			return err
		}
		if !safe {
			return fmt.Errorf("%w: unexpected settings, ownership or direct grants on %s", audit.ErrRefused, result.Login)
		}
	} else {
		if _, err := tx.Exec(ctx, "CREATE ROLE "+login+" LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "GRANT "+group+" TO "+login+" WITH INHERIT TRUE, SET TRUE, ADMIN FALSE"); err != nil {
			return err
		}
	}
	// Migrations and SQL models belong to their durable permission groups,
	// never to a login that an operator must later rotate or replace.
	if _, err := tx.Exec(ctx, "ALTER ROLE "+login+" SET role TO "+group); err != nil {
		return err
	}
	var database string
	if err := tx.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "GRANT CONNECT ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+group); err != nil {
		return err
	}
	// PostgreSQL utility statements cannot bind parameters; pgx's simple protocol
	// quotes the password after native statement logging has been disabled.
	_, err := tx.Exec(ctx, "ALTER ROLE "+login+" PASSWORD $1", pgx.QueryExecModeSimpleProtocol, password)
	return err
}
