package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"
)

func backupLogin(t *testing.T, admin *pgx.Conn, database *url.URL) (string, func()) {
	t.Helper()
	ctx := t.Context()
	name := "ddp_backup_test_" + strings.ToLower(ulid.Make().String())
	password := "backup-test-password-" + ulid.Make().String()
	query := fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", pgx.Identifier{name}.Sanitize(), strings.ReplaceAll(password, "'", "''"))
	if _, err := admin.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT ddp_backup TO "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	u := *database
	u.User = url.UserPassword(name, password)
	return u.String(), func() {
		if _, err := admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Errorf("drop backup test login: %v", err)
		}
	}
}

func TestBackupRolePrivileges(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	database := "ddp_backup_privileges_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop privilege test database: %v", err)
		}
	})
	dbURL, _ := url.Parse(raw)
	dbURL.Path = "/" + database
	conn, err := pgx.Connect(ctx, dbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		conn.Close(context.Background())
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `CREATE TABLE app.privilege_probe(id integer PRIMARY KEY, value text NOT NULL);
ALTER TABLE app.privilege_probe OWNER TO ddp_owner;
GRANT SELECT ON app.privilege_probe TO ddp_api;
INSERT INTO app.privilege_probe VALUES (1,'fixture')`); err != nil {
		conn.Close(context.Background())
		t.Fatal(err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var login, groupLogin, super, createdb, createrole, bypass, replication bool
	if err := admin.QueryRow(ctx, `SELECT r.rolcanlogin, g.rolcanlogin, r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolbypassrls, r.rolreplication
FROM pg_roles r JOIN pg_roles g ON g.rolname='ddp_backup' WHERE r.rolname=$1`, "ddp_backup").Scan(&login, &groupLogin, &super, &createdb, &createrole, &bypass, &replication); err != nil {
		t.Fatal(err)
	}
	if login || groupLogin || super || createdb || createrole || bypass || replication {
		t.Fatalf("unsafe backup group attributes: login=%t group_login=%t super=%t createdb=%t createrole=%t bypass=%t replication=%t", login, groupLogin, super, createdb, createrole, bypass, replication)
	}
	backupURL, cleanup := backupLogin(t, admin, dbURL)
	defer cleanup()
	backup, err := pgx.Connect(ctx, backupURL)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close(context.Background())
	var roleSuper, roleCreatedb, roleCreaterole, roleBypass, roleReplication, canlogin bool
	if err := backup.QueryRow(ctx, `SELECT rolsuper,rolcreatedb,rolcreaterole,rolbypassrls,rolreplication,rolcanlogin FROM pg_roles WHERE rolname=current_user`).Scan(&roleSuper, &roleCreatedb, &roleCreaterole, &roleBypass, &roleReplication, &canlogin); err != nil {
		t.Fatal(err)
	}
	if roleSuper || roleCreatedb || roleCreaterole || roleBypass || roleReplication || !canlogin {
		t.Fatalf("unsafe backup role attributes: super=%t createdb=%t createrole=%t bypass=%t replication=%t canlogin=%t", roleSuper, roleCreatedb, roleCreaterole, roleBypass, roleReplication, canlogin)
	}
	var memberships []string
	if err := backup.QueryRow(ctx, `SELECT coalesce(array_agg(parent.rolname ORDER BY parent.rolname),'{}') FROM pg_auth_members m JOIN pg_roles child ON child.oid=m.member JOIN pg_roles parent ON parent.oid=m.roleid WHERE child.rolname=current_user`).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || memberships[0] != "ddp_backup" {
		t.Fatalf("unexpected backup memberships: %v", memberships)
	}
	if _, err := backup.Exec(ctx, `INSERT INTO ops.backups(id,status,deadline_at) VALUES('privilege-proof','running',clock_timestamp()+interval '1 hour');
UPDATE ops.backups SET status='succeeded',finished_at=clock_timestamp(),manifest='{}' WHERE id='privilege-proof';
INSERT INTO ddp.audit(id,action,target,outcome) VALUES('privilege-proof','backup.test','backup/privilege-proof','{}')`); err != nil {
		t.Fatalf("backup evidence writes refused: %v", err)
	}
	var count int
	if err := backup.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.users)+(SELECT count(*) FROM app.privilege_probe)`).Scan(&count); err != nil || count < 1 {
		t.Fatalf("backup role cannot read auth and client data: %v", err)
	}
	denied := map[string]string{
		"client write":            `INSERT INTO app.privilege_probe VALUES (2,'x')`,
		"client ddl":              `CREATE TABLE app.backup_forbidden(id integer)`,
		"auth write":              `UPDATE app.users SET email='forged@example.test' WHERE false`,
		"restore evidence":        `INSERT INTO ops.backup_restores(id,backup_id,target_database,status,deadline_at) VALUES('x','x','x','succeeded',clock_timestamp())`,
		"audit principal forgery": `INSERT INTO ddp.audit(id,principal,action,target,outcome) VALUES('x','forged','backup.test','x','{}')`,
		"create database":         `CREATE DATABASE backup_forbidden_db`,
		"create role":             `CREATE ROLE backup_forbidden_role`,
		"set owner":               `SET ROLE ddp_owner`,
		"set scheduler":           `SET ROLE ddp_scheduler`,
	}
	for name, query := range denied {
		t.Run(name, func(t *testing.T) {
			if _, err := backup.Exec(ctx, query); err == nil {
				t.Fatalf("query unexpectedly succeeded: %s", query)
			} else {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || (pgErr.Code != "42501" && pgErr.Code != "0LP01") {
					t.Fatalf("query failed for wrong reason: %v", err)
				}
			}
		})
	}
}
