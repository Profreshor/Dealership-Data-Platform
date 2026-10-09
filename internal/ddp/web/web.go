package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/jackc/pgx/v5/pgxpool"
)

type policyKind uint8
type Policy struct {
	kind policyKind
	name string
}

const (
	public policyKind = iota + 1
	authenticated
	permission
	admin
)

func Public() Policy                { return Policy{kind: public} }
func Authenticated() Policy         { return Policy{kind: authenticated} }
func Permission(name string) Policy { return Policy{kind: permission, name: name} }
func Admin() Policy                 { return Policy{kind: admin} }
func (p Policy) valid() bool {
	return p.kind >= public && p.kind <= admin && (p.kind != permission || strings.TrimSpace(p.name) != "")
}

type route struct {
	pattern string
	handler http.Handler
	policy  Policy
}
type Registry struct {
	// Pool is the API database pool. It is nil during route discovery and validation.
	Pool   *pgxpool.Pool
	routes []route
}

type RouteInfo struct {
	Pattern string `json:"pattern"`
	Policy  string `json:"policy"`
}

// Routes describes the same registrations used to build the HTTP handler.
func (r *Registry) Routes() []RouteInfo {
	out := make([]RouteInfo, 0, len(r.routes))
	for _, route := range r.routes {
		policy := "invalid"
		switch route.policy.kind {
		case public:
			policy = "public"
		case authenticated:
			policy = "authenticated"
		case permission:
			policy = "permission:" + route.policy.name
		case admin:
			policy = "admin"
		}
		out = append(out, RouteInfo{Pattern: route.pattern, Policy: policy})
	}
	slices.SortFunc(out, func(a, b RouteInfo) int { return strings.Compare(a.Pattern, b.Pattern) })
	return out
}

func NewRegistry() *Registry { return &Registry{} }
func (r *Registry) Handle(pattern string, handler http.Handler, policy Policy) {
	r.routes = append(r.routes, route{pattern, handler, policy})
}
func (r *Registry) Build(g *Guard) (out http.Handler, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = errors.New("invalid route pattern")
		}
	}()
	seen := map[string]bool{}
	mux := http.NewServeMux()
	for _, x := range r.routes {
		if x.pattern == "" || x.handler == nil || !x.policy.valid() {
			return nil, errors.New("invalid route")
		}
		if x.policy.kind != public && (g == nil || g.Auth == nil) {
			return nil, errors.New("protected route requires auth guard")
		}
		if seen[x.pattern] {
			return nil, errors.New("duplicate route")
		}
		seen[x.pattern] = true
		mux.Handle(x.pattern, g.wrap(x.policy, x.handler))
	}
	return mux, nil
}

// Origin is the configured public origin used for browser request checks.
type Guard struct {
	Auth       *auth.Service
	CookieName string
	Secure     bool
	Origin     string
}

func (g *Guard) wrap(p Policy, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.kind == public {
			h.ServeHTTP(w, r)
			return
		}
		sess, ok := g.session(r)
		if !ok {
			httpx.Fail(w, 401, "unauthorized", "Unauthorized")
			return
		}
		if p.kind == permission && !contains(sess.User.Permissions, p.name) && !sess.User.Admin {
			httpx.Fail(w, 403, "forbidden", "Forbidden")
			return
		}
		if p.kind == admin && !sess.User.Admin {
			httpx.Fail(w, 403, "forbidden", "Forbidden")
			return
		}
		if unsafe(r) && !sameOrigin(g, r) {
			httpx.Fail(w, 403, "origin", "Origin rejected")
			return
		}
		if unsafe(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRFToken)) != 1 {
			httpx.Fail(w, 403, "csrf", "CSRF token required")
			return
		}
		h.ServeHTTP(w, r.WithContext(WithSession(r.Context(), sess)))
	})
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func unsafe(r *http.Request) bool {
	return r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS"
}
func (g *Guard) session(r *http.Request) (auth.Session, bool) {
	c, e := r.Cookie(g.cookie())
	if e != nil {
		return auth.Session{}, false
	}
	s, e := g.Auth.Session(r.Context(), c.Value)
	return s, e == nil
}
func (g *Guard) cookie() string {
	if g.CookieName != "" {
		return g.CookieName
	}
	return "ddp_session"
}

type sessionKey struct{}

func WithSession(ctx context.Context, s auth.Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}
func CurrentSession(ctx context.Context) (auth.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(auth.Session)
	return s, ok
}
