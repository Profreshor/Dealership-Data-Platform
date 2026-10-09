package scaffold

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestBuildRoute(t *testing.T) {
	r := []byte("registry: unchanged\n")
	p, err := buildRoute(r, Request{Name: "customers", Definition: []byte("pattern: GET /api/customers\npolicy: public\n")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Ref != "route/customers" || string(p.Registry) != string(r) || !strings.Contains(string(p.Files["internal/app/customers/register.go"]), "web.Public()") {
		t.Fatalf("bad route proposal: %#v", p)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "register.go", p.Files["internal/app/customers/register.go"], 0); err != nil {
		t.Fatal(err)
	}
}

func TestAddRouteRegistration(t *testing.T) {
	source := []byte("package app\n\nimport (\n\t\"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web\"\n)\n\nfunc Register(named *web.Registry) {\n\t// existing\n}\n")
	out, err := addRouteRegistration(source, "customers")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "route_customers \"github.com/Profreshor/Dealership-Data-Platform/internal/app/customers\"") || !strings.Contains(got, "route_customers.Register(named)") || !strings.Contains(got, "// existing") {
		t.Fatalf("registration not inserted: %s", got)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "register.go", out, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteRegistration(out, "customers"); err == nil {
		t.Fatal("duplicate feature accepted")
	}
}

func TestAddRouteRegistrationRejectsUnsafeShapes(t *testing.T) {
	base := `package app
import w "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *w.Registry) {}`
	for _, src := range []string{
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *web.Registry)`,
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(route_customers *web.Registry) {}`,
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *web.Registry) { if true {} }`,
		`package app
import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *web.Registry) { foo.Register(reg...) }`,
		`package app
import w "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
func Register(reg *wrong.Registry) {}`,
	} {
		if _, err := addRouteRegistration([]byte(src), "customers"); err == nil {
			t.Fatalf("accepted unsafe source: %s", src)
		}
	}
	out, err := addRouteRegistration([]byte(base), "customers")
	if err != nil || !strings.Contains(string(out), "route_customers.Register(reg)") {
		t.Fatalf("failed valid named alias source: %v\n%s", err, out)
	}
}
