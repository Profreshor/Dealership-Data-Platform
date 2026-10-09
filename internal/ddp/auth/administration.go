package auth

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const administrationLimit = 1000

type Account struct {
	ID       string   `json:"id"`
	Email    string   `json:"email"`
	Admin    bool     `json:"admin"`
	Disabled bool     `json:"disabled"`
	Pending  bool     `json:"pending"`
	Roles    []string `json:"roles"`
}

type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type AccountDirectory struct {
	Users       []Account `json:"users"`
	Roles       []Role    `json:"roles"`
	Permissions []string  `json:"permissions"`
}

func (s *Service) Directory(ctx context.Context, cfg *config.Config) (AccountDirectory, error) {
	d := AccountDirectory{Users: []Account{}, Roles: []Role{}, Permissions: []string{}}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return d, accountError(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,email,is_admin,disabled_at IS NOT NULL,password_hash IS NULL,ARRAY(SELECT role_id FROM app.user_roles WHERE user_id=app.users.id ORDER BY role_id) FROM app.users ORDER BY id LIMIT 1001`)
	if err != nil {
		return d, accountError(err)
	}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.Admin, &a.Disabled, &a.Pending, &a.Roles); err != nil {
			return d, accountError(err)
		}
		d.Users = append(d.Users, a)
	}
	if err := rows.Err(); err != nil {
		return d, accountError(err)
	}
	rows.Close()
	if len(d.Users) > administrationLimit {
		return AccountDirectory{}, ErrInvalidAccount
	}
	roles, err := tx.Query(ctx, `SELECT id,name,ARRAY(SELECT permission FROM app.role_permissions WHERE role_id=app.roles.id ORDER BY permission) FROM app.roles ORDER BY id LIMIT 1001`)
	if err != nil {
		return d, accountError(err)
	}
	defer roles.Close()
	for roles.Next() {
		var r Role
		if err := roles.Scan(&r.ID, &r.Name, &r.Permissions); err != nil {
			return d, accountError(err)
		}
		d.Roles = append(d.Roles, r)
	}
	if err := roles.Err(); err != nil {
		return d, accountError(err)
	}
	roles.Close()
	if len(d.Roles) > administrationLimit {
		// ponytail: bounded directory; add pagination only when a deployment needs more than 1000 accounts.
		return AccountDirectory{}, ErrInvalidAccount
	}
	if cfg != nil {
		for p := range cfg.Permissions {
			d.Permissions = append(d.Permissions, p)
		}
	}
	if !slices.Contains(d.Permissions, "users.manage") {
		d.Permissions = append(d.Permissions, "users.manage")
	}
	slices.Sort(d.Permissions)
	return d, nil
}

func normalizeRoles(roles []string) ([]string, error) {
	if len(roles) > 100 {
		return nil, ErrInvalidAccount
	}
	r := slices.Clone(roles)
	slices.Sort(r)
	r = slices.Compact(r)
	if r == nil {
		r = []string{}
	}
	for _, id := range r {
		if id == "" || len(id) > 128 {
			return nil, ErrInvalidAccount
		}
	}
	return r, nil
}

func (s *Service) UpdateAccount(ctx context.Context, id string, roles []string, disabled bool, actor string) error {
	roles, err := normalizeRoles(roles)
	if id == "" || len(id) > 128 || len(actor) > 26 || err != nil {
		return ErrInvalidAccount
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ddp account administration', 0))`); err != nil {
			return err
		}
		var admin, oldDisabled bool
		if err := tx.QueryRow(ctx, `SELECT is_admin,disabled_at IS NOT NULL FROM app.users WHERE id=$1 FOR UPDATE`, id).Scan(&admin, &oldDisabled); errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidAccount
		} else if err != nil {
			return err
		}
		if admin {
			return audit.ErrRefused
		}
		for _, role := range roles {
			var found string
			if err := tx.QueryRow(ctx, `SELECT id FROM app.roles WHERE id=$1 FOR KEY SHARE`, role).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidAccount
			} else if err != nil {
				return err
			}
		}
		var old []string
		rows, err := tx.Query(ctx, `SELECT role_id FROM app.user_roles WHERE user_id=$1 ORDER BY role_id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				rows.Close()
				return err
			}
			old = append(old, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		changed := oldDisabled != disabled || !slices.Equal(old, roles)
		if !changed {
			return audit.Record(ctx, tx, "users.update", "user/"+id, map[string]any{"actor_user_id": actor, "roles": roles, "disabled": disabled})
		}
		if disabled {
			_, err = tx.Exec(ctx, `UPDATE app.users SET disabled_at=COALESCE(disabled_at,clock_timestamp()) WHERE id=$1`, id)
		} else {
			_, err = tx.Exec(ctx, `UPDATE app.users SET disabled_at=NULL WHERE id=$1`, id)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.user_roles WHERE user_id=$1`, id); err != nil {
			return err
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx, `INSERT INTO app.user_roles(user_id,role_id) VALUES($1,$2)`, id, role); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.password_tokens WHERE user_id=$1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "users.update", "user/"+id, map[string]any{"actor_user_id": actor, "roles": roles, "disabled": disabled})
	})
	return accountError(err)
}

func (s *Service) SaveRole(ctx context.Context, cfg *config.Config, id, name string, permissions []string, actor string) error {
	name = strings.TrimSpace(name)
	if id == "" || len(id) > 128 || name == "" || len(name) > 128 || len(actor) > 26 || len(permissions) > 100 {
		return ErrInvalidAccount
	}
	permissions = slices.Clone(permissions)
	slices.Sort(permissions)
	permissions = slices.Compact(permissions)
	if permissions == nil {
		permissions = []string{}
	}
	for _, p := range permissions {
		if len(p) == 0 || len(p) > 128 {
			return ErrInvalidAccount
		}
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// ponytail: one global account lock; split by account/role only when contention is measured.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ddp account administration', 0))`); err != nil {
			return err
		}
		known := map[string]bool{"users.manage": true}
		if cfg != nil {
			for p := range cfg.Permissions {
				known[p] = true
			}
		}
		for _, p := range permissions {
			if !known[p] {
				return ErrInvalidAccount
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.roles(id,name) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name`, id, name); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) && postgresError.Code == "23505" {
				return ErrInvalidAccount
			}
			return err
		}
		for _, p := range permissions {
			if _, err := tx.Exec(ctx, `INSERT INTO app.permissions(name) VALUES($1) ON CONFLICT DO NOTHING`, p); err != nil {
				return err
			}
		}
		var old []string
		rows, err := tx.Query(ctx, `SELECT permission FROM app.role_permissions WHERE role_id=$1 ORDER BY permission`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			old = append(old, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		changed := !slices.Equal(old, permissions)
		if changed {
			rows, err = tx.Query(ctx, `SELECT u.id FROM app.users u JOIN app.user_roles ur ON ur.user_id=u.id WHERE ur.role_id=$1 ORDER BY u.id FOR UPDATE`, id)
			if err != nil {
				return err
			}
			var users []string
			for rows.Next() {
				var uid string
				if err := rows.Scan(&uid); err != nil {
					rows.Close()
					return err
				}
				users = append(users, uid)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			if _, err := tx.Exec(ctx, `DELETE FROM app.role_permissions WHERE role_id=$1`, id); err != nil {
				return err
			}
			for _, p := range permissions {
				if _, err := tx.Exec(ctx, `INSERT INTO app.role_permissions(role_id,permission) VALUES($1,$2)`, id, p); err != nil {
					return err
				}
			}
			for _, uid := range users {
				if _, err := tx.Exec(ctx, `DELETE FROM app.sessions WHERE user_id=$1`, uid); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM app.password_tokens WHERE user_id=$1`, uid); err != nil {
					return err
				}
			}
		}
		return audit.Record(ctx, tx, "roles.save", "role/"+id, map[string]any{"actor_user_id": actor, "name": name, "permissions": permissions})
	})
	return accountError(err)
}
