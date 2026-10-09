package scaffold

import (
	"strings"
	"testing"
)

func TestRouteDefinitionsCoverPoliciesAndRejectMalformedInput(t *testing.T) {
	registry := []byte(strings.Replace(string(baseRegistry(t)), "permissions: {}", "permissions:\n  customers.read: {description: Read customers}", 1))
	for policy, expression := range map[string]string{"public": "web.Public()", "authenticated": "web.Authenticated()", "admin": "web.Admin()", "permission:customers.read": `web.Permission("customers.read")`} {
		p, err := buildRoute(registry, Request{Name: "customers", Definition: []byte("pattern: GET /api/customers\npolicy: " + policy + "\n")})
		if err != nil {
			t.Fatalf("policy %q: %v", policy, err)
		}
		if !strings.Contains(string(p.Files["internal/app/customers/register.go"]), expression) {
			t.Fatalf("policy %q did not reach generated handler", policy)
		}
	}
	for _, tc := range []struct {
		name string
		def  string
	}{
		{"unknown permission", "pattern: GET /api/x\npolicy: permission:nope\n"},
		{"duplicate field", "pattern: GET /api/x\npattern: GET /api/y\npolicy: public\n"},
		{"unknown field", "pattern: GET /api/x\npolicy: public\nextra: x\n"},
		{"nonstring value", "pattern: GET /api/x\npolicy: 1\n"},
		{"alias", "pattern: &p GET /api/x\npolicy: *p\n"},
		{"multidoc", "pattern: GET /api/x\npolicy: public\n---\npattern: GET /api/y\npolicy: public\n"},
		{"wrong method", "pattern: FOO /api/x\npolicy: public\n"},
		{"wrong path", "pattern: GET /private/x\npolicy: public\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRoute(registry, Request{Name: "customers", Definition: []byte(tc.def)}); err == nil {
				t.Fatalf("accepted malformed route definition: %s", tc.def)
			}
		})
	}
	for _, name := range []string{"main", "break", "../escape", "a/b", ""} {
		if _, err := buildRoute(registry, Request{Name: name, Definition: []byte("pattern: GET /api/x\npolicy: public\n")}); err == nil {
			t.Fatalf("accepted unsafe route name %q", name)
		}
	}
}

func TestRouteRegistrationRejectsBareBlankAndRepeatedFeatures(t *testing.T) {
	for _, source := range []string{
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *web.Registry)`,
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(_ *web.Registry) {}`,
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *web.Registry) { Register(reg) }`,
	} {
		if _, err := addRouteRegistration([]byte(source), "customers"); err == nil {
			t.Fatalf("accepted unsafe aggregator: %s", source)
		}
	}
	source := []byte("package app\nimport \"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web\"\nfunc Register(reg *web.Registry) {}\n")
	out, err := addRouteRegistration(source, "customers")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteRegistration(out, "customers"); err == nil {
		t.Fatal("accepted repeated feature")
	}
	out, err = addRouteRegistration(out, "invoices")
	if err != nil || !strings.Contains(string(out), "route_customers.Register(reg)") || !strings.Contains(string(out), "route_invoices.Register(reg)") {
		t.Fatalf("adding second feature lost registration: %s (%v)", out, err)
	}
	// A compact import declaration is valid Go and must remain compilable.
	compact := []byte("package app; import \"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web\"; func Register(reg *web.Registry) {}")
	if _, err := addRouteRegistration(compact, "compact"); err != nil {
		t.Fatalf("compact aggregator rejected: %v", err)
	}
}
