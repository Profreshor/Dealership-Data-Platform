package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrInvalidLink = errors.New("invalid or expired password link")
var ErrInvalidAccount = errors.New("invalid account details")

type Invitation struct {
	ID     string   `json:"id"`
	Email  string   `json:"email"`
	Roles  []string `json:"roles"`
	Status string   `json:"status"`
}

// accountOrigin uses configured deployment facts, never an incoming Host header.
func accountOrigin(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", errors.New("account email requires configuration")
	}
	if err := config.ValidateSMTP(cfg.Comms.SMTP); err != nil {
		return "", err
	}
	u, err := url.Parse(cfg.Serving.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return "", errors.New("account email requires an HTTPS public URL or loopback HTTP URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

func accountError(err error) error {
	if err == nil || errors.Is(err, ErrInvalidAccount) || errors.Is(err, ErrInvalidLink) || errors.Is(err, audit.ErrRefused) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return audit.ErrRefused
	}
	return errors.New("account database operation failed")
}

func (s *Service) Invite(ctx context.Context, cfg *config.Config, email string, roles []string, actor string) (Invitation, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) || len(roles) > 100 || len(actor) > 26 {
		return Invitation{}, ErrInvalidAccount
	}
	roles = slices.Clone(roles)
	slices.Sort(roles)
	roles = slices.Compact(roles)
	if roles == nil {
		roles = []string{}
	}
	for _, role := range roles {
		if role == "" || len(role) > 128 {
			return Invitation{}, ErrInvalidAccount
		}
	}
	origin, err := accountOrigin(cfg)
	if err != nil {
		return Invitation{}, err
	}
	result := Invitation{Email: email, Roles: roles, Status: "invited"}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ddp account administration', 0))`); err != nil {
			return err
		}
		// The unique email insert serializes concurrent first invitations; all existing
		// account operations then lock the user before touching its token or sessions.
		if _, err := tx.Exec(ctx, `INSERT INTO app.users(id,email) VALUES($1,$2) ON CONFLICT(email) DO NOTHING`, newID(), email); err != nil {
			return err
		}
		var pending, disabled, admin bool
		if err := tx.QueryRow(ctx, `SELECT id,password_hash IS NULL,disabled_at IS NOT NULL,is_admin FROM app.users WHERE email=$1 FOR UPDATE`, email).Scan(&result.ID, &pending, &disabled, &admin); err != nil {
			return err
		}
		if !pending || disabled || admin {
			return audit.ErrRefused
		}
		for _, role := range roles {
			var id string
			if err := tx.QueryRow(ctx, `SELECT id FROM app.roles WHERE id=$1 FOR KEY SHARE`, role).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidAccount
			} else if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.user_roles WHERE user_id=$1`, result.ID); err != nil {
			return err
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx, `INSERT INTO app.user_roles(user_id,role_id) VALUES($1,$2)`, result.ID, role); err != nil {
				return err
			}
		}
		if err := issueLink(ctx, tx, cfg, origin, result.ID, email, "invite"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "users.invite", "user/"+result.ID, map[string]any{"status": "invited", "roles": roles, "actor_user_id": actor})
	})
	if err != nil {
		return Invitation{}, accountError(err)
	}
	return result, nil
}

func issueLink(ctx context.Context, tx pgx.Tx, cfg *config.Config, origin, id, email, kind string) error {
	token, hash, err := randomToken()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.password_tokens WHERE user_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.password_tokens(token_hash,user_id,kind) VALUES($1,$2,$3)`, hash, id, kind); err != nil {
		return err
	}
	template, key, path := "password-reset", "ResetURL", "/reset-password"
	if kind == "invite" {
		template, key, path = "welcome", "LoginURL", "/welcome"
	}
	// Authentication state stores only the hash. The private email outbox, like
	// the recipient's mailbox, necessarily contains the expiring capability URL.
	_, err = comms.Enqueue(ctx, tx, cfg.Comms, comms.Input{EffectKey: "auth/" + kind + "/" + newID(), Template: template, Recipients: []string{email}, Context: map[string]any{"Name": "there", key: origin + path + "#token=" + token, "ExpiresIn": "in 1 hour"}})
	return err
}

func (s *Service) RequestReset(ctx context.Context, cfg *config.Config, email string) error {
	origin, err := accountOrigin(cfg)
	if err != nil {
		return err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return nil
	}
	if !s.allowed("reset:" + email) {
		return ErrRateLimited
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM app.users WHERE email=$1 AND disabled_at IS NULL AND password_hash IS NOT NULL FOR UPDATE`, email).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var recent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.password_tokens WHERE user_id=$1 AND created_at > clock_timestamp()-interval '1 minute')`, id).Scan(&recent); err != nil {
			return err
		}
		if recent {
			return nil
		}
		return issueLink(ctx, tx, cfg, origin, id, email, "reset")
	})
	return accountError(err)
}

func (s *Service) SetPassword(ctx context.Context, cfg *config.Config, token, password string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if len(token) != 43 || err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return ErrInvalidLink
	}
	if !validPassword(password) {
		return ErrInvalidAccount
	}
	if !s.allowed(fmt.Sprintf("link:%x", HashToken(token))) {
		return ErrRateLimited
	}
	origin, err := accountOrigin(cfg)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var id, email, kind string
		if err := tx.QueryRow(ctx, `SELECT user_id FROM app.password_tokens WHERE token_hash=$1`, HashToken(token)).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidLink
		} else if err != nil {
			return err
		}
		var pending, disabled bool
		if err := tx.QueryRow(ctx, `SELECT email,password_hash IS NULL,disabled_at IS NOT NULL FROM app.users WHERE id=$1 FOR UPDATE`, id).Scan(&email, &pending, &disabled); errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidLink
		} else if err != nil {
			return err
		}
		if disabled {
			return ErrInvalidLink
		}
		if err := tx.QueryRow(ctx, `SELECT kind FROM app.password_tokens WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR UPDATE`, HashToken(token), id).Scan(&kind); errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidLink
		} else if err != nil {
			return err
		}
		if pending != (kind == "invite") {
			return ErrInvalidLink
		}
		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE app.users SET password_hash=$1 WHERE id=$2`, hash, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.password_tokens WHERE user_id=$1`, id); err != nil {
			return err
		}
		if _, err := comms.Enqueue(ctx, tx, cfg.Comms, comms.Input{EffectKey: "auth/password-changed/" + newID(), Template: "password-changed", Recipients: []string{email}, Context: map[string]any{"LoginURL": origin + "/login"}}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "users.password_set", "user/"+id, map[string]string{"kind": kind, "status": "changed"})
	})
	return accountError(err)
}
