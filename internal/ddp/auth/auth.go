package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/argon2"
)

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrRateLimited = errors.New("too many login attempts")

type User struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Admin       bool     `json:"admin"`
	Permissions []string `json:"permissions"`
}
type Session struct {
	User      User   `json:"user"`
	CSRFToken string `json:"csrf_token"`
}
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type Service struct {
	DB       *pgxpool.Pool
	TTL      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	attempts map[string][]time.Time
}

var dummyHash, _ = HashPassword("ddp-dummy-password")

func New(db *pgxpool.Pool, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &Service{DB: db, TTL: ttl, now: time.Now, attempts: make(map[string][]time.Time)}
}

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, e := mail.ParseAddress(s)
	return e == nil && a.Address == s && strings.Contains(s, "@")
}
func validPassword(s string) bool { return len(s) >= 8 && len(s) <= 128 }
func (s *Service) Bootstrap(ctx context.Context, email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) || !validPassword(password) {
		return errors.New("invalid administrator credentials")
	}
	h, e := HashPassword(password)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return bootstrapError(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ddp bootstrap admin', 0))`); e != nil {
		return bootstrapError(e)
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ddp account administration', 0))`); e != nil {
		return bootstrapError(e)
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE is_admin`).Scan(&count); e != nil {
		return bootstrapError(e)
	}
	if count != 0 {
		return errors.New("administrator already bootstrapped")
	}
	id := newID()
	status := "created"
	var pendingID string
	var pending, disabled, existingAdmin bool
	lookupErr := tx.QueryRow(ctx, `SELECT id,password_hash IS NULL,disabled_at IS NOT NULL,is_admin FROM app.users WHERE email=$1 FOR UPDATE`, email).Scan(&pendingID, &pending, &disabled, &existingAdmin)
	if lookupErr == nil {
		if !pending || disabled || existingAdmin {
			return errors.New("administrator account already exists")
		}
		id = pendingID
		status = "activated"
		if _, e = tx.Exec(ctx, `DELETE FROM app.password_tokens WHERE user_id=$1`, id); e != nil {
			return bootstrapError(e)
		}
		if _, e = tx.Exec(ctx, `DELETE FROM app.sessions WHERE user_id=$1`, id); e != nil {
			return bootstrapError(e)
		}
		if _, e = tx.Exec(ctx, `UPDATE app.users SET password_hash=$1,is_admin=true WHERE id=$2`, h, id); e != nil {
			return bootstrapError(e)
		}
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return bootstrapError(lookupErr)
	} else if _, e = tx.Exec(ctx, `INSERT INTO app.users(id,email,password_hash,is_admin) VALUES($1,$2,$3,true)`, id, email, h); e != nil {
		return bootstrapError(e)
	}
	if e = audit.Record(ctx, tx, "users.bootstrap", "user/"+id, map[string]string{"status": status}); e != nil {
		return e
	}
	return bootstrapError(tx.Commit(ctx))
}

func bootstrapError(err error) error {
	if err == nil || errors.Is(err, audit.ErrRefused) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if pe.Code == "42501" {
			return audit.ErrRefused
		}
		return fmt.Errorf("bootstrap database error: postgres %s", pe.Code)
	}
	return errors.New("bootstrap database operation failed")
}

func newID() string {
	return ulid.Make().String()
}
func randomToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, HashToken(t), nil
}
func HashToken(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }

func HashPassword(password string) (string, error) {
	if !validPassword(password) {
		return "", errors.New("password must be 8-128 bytes")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	out := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=4$%s$%s", enc.EncodeToString(salt), enc.EncodeToString(out)), nil
}
func VerifyPassword(encoded, password string) bool {
	if len(encoded) > 512 || !validPassword(password) {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var m, t, p int
	if n, e := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); e != nil || n != 3 || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) || m != 64*1024 || t != 3 || p != 4 {
		return false
	}
	ss, hs := parts[4], parts[5]
	salt, e := base64.RawStdEncoding.DecodeString(ss)
	if e != nil || len(salt) < 8 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(hs)
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Service) allowed(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attempts[key]; !ok && len(s.attempts) >= 4096 {
		// ponytail: bounded map with arbitrary eviction; use a shared limiter if traffic requires stronger fairness.
		for old := range s.attempts {
			delete(s.attempts, old)
			break
		}
	}
	now := s.now()
	xs := s.attempts[key]
	j := 0
	for _, t := range xs {
		if now.Sub(t) < time.Minute {
			xs[j] = t
			j++
		}
	}
	xs = xs[:j]
	if len(xs) >= 10 {
		s.attempts[key] = xs
		return false
	}
	s.attempts[key] = append(xs, now)
	return true
}
func (s *Service) Login(ctx context.Context, email, password string) (string, Session, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) || !validPassword(password) {
		return "", Session{}, ErrInvalidCredentials
	}
	if !s.allowed(email) {
		return "", Session{}, ErrRateLimited
	}
	var id, hash string
	var admin, disabled, pending bool
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", Session{}, accountError(e)
	}
	defer tx.Rollback(ctx)
	// Serialize credential verification and session creation with password reset.
	e = tx.QueryRow(ctx, `SELECT id,email,COALESCE(password_hash,$2),is_admin,disabled_at IS NOT NULL,password_hash IS NULL FROM app.users WHERE email=$1 FOR UPDATE`, email, dummyHash).Scan(&id, &email, &hash, &admin, &disabled, &pending)
	if e != nil {
		_ = VerifyPassword(dummyHash, password)
		return "", Session{}, ErrInvalidCredentials
	}
	matches := VerifyPassword(hash, password)
	if pending || disabled || !matches {
		return "", Session{}, ErrInvalidCredentials
	}
	tok, h, e := randomToken()
	if e != nil {
		return "", Session{}, e
	}
	csrf := csrfFor(tok)
	if _, e = tx.Exec(ctx, `INSERT INTO app.sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, h, id, s.now().Add(s.TTL)); e != nil {
		return "", Session{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", Session{}, accountError(e)
	}
	sess, e := s.session(ctx, id, email, admin, csrf)
	return tok, sess, e
}
func (s *Service) session(ctx context.Context, id, email string, admin bool, csrf string) (Session, error) {
	rows, e := s.DB.Query(ctx, `SELECT DISTINCT rp.permission FROM app.user_roles ur JOIN app.role_permissions rp ON rp.role_id=ur.role_id WHERE ur.user_id=$1 ORDER BY rp.permission`, id)
	if e != nil {
		return Session{}, e
	}
	defer rows.Close()
	p := []string{}
	for rows.Next() {
		var x string
		if e = rows.Scan(&x); e != nil {
			return Session{}, e
		}
		p = append(p, x)
	}
	return Session{User: User{ID: id, Email: email, Admin: admin, Permissions: p}, CSRFToken: csrf}, rows.Err()
}
func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	var id, email string
	var admin bool
	e := s.DB.QueryRow(ctx, `SELECT u.id,u.email,u.is_admin FROM app.sessions x JOIN app.users u ON u.id=x.user_id WHERE x.token_hash=$1 AND x.expires_at>now() AND u.disabled_at IS NULL`, HashToken(token)).Scan(&id, &email, &admin)
	if e != nil {
		return Session{}, e
	}
	csrf := csrfFor(token)
	return s.session(ctx, id, email, admin, csrf)
}

func csrfFor(token string) string {
	h := sha256.Sum256(append([]byte("csrf:"), []byte(token)...))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, e := s.DB.Exec(ctx, `DELETE FROM app.sessions WHERE token_hash=$1`, HashToken(token))
	return e
}
