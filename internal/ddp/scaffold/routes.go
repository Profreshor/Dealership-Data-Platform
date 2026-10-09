package scaffold

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"go.yaml.in/yaml/v3"
)

func buildRoute(registry []byte, r Request) (Proposal, error) {
	if err := validateName("route", r.Name); err != nil || !token.IsIdentifier(r.Name) || r.Name == "main" {
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{}, fmt.Errorf("invalid route name %q", r.Name)
	}
	if r.Source != nil {
		return Proposal{}, fmt.Errorf("route source is unsupported")
	}
	v, err := parseValue(r.Definition, "route definition")
	if err != nil {
		return Proposal{}, err
	}
	if v.Kind != yaml.MappingNode {
		return Proposal{}, fmt.Errorf("route definition must be a YAML mapping")
	}
	if err := rejectAliases(v); err != nil {
		return Proposal{}, err
	}
	var pattern, policy string
	seen := map[string]bool{}
	for i := 0; i < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return Proposal{}, fmt.Errorf("route definition keys must be strings")
		}
		if k.Value != "pattern" && k.Value != "policy" {
			return Proposal{}, fmt.Errorf("unknown route definition field %q", k.Value)
		}
		if seen[k.Value] {
			return Proposal{}, fmt.Errorf("duplicate route definition field %q", k.Value)
		}
		seen[k.Value] = true
		if val.Kind != yaml.ScalarNode || val.Tag != "!!str" {
			return Proposal{}, fmt.Errorf("route definition field %s must be a string", k.Value)
		}
		if k.Value == "pattern" {
			pattern = val.Value
		} else {
			policy = val.Value
		}
	}
	if !seen["pattern"] || !seen["policy"] {
		return Proposal{}, fmt.Errorf("route definition requires pattern and policy")
	}
	if err := validateRoutePattern(pattern); err != nil {
		return Proposal{}, err
	}
	if err := validateRoutePolicy(policy, registry); err != nil {
		return Proposal{}, err
	}
	return Proposal{Kind: "route", Ref: "route/" + r.Name, Registry: append([]byte(nil), registry...), Files: map[string][]byte{"internal/app/" + r.Name + "/register.go": routeSource(r.Name, pattern, policy)}}, nil
}

func validateRoutePattern(pattern string) error {
	parts := strings.SplitN(pattern, " ", 2)
	methods := map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "CONNECT": true, "OPTIONS": true, "TRACE": true}
	if len(parts) != 2 || !methods[parts[0]] || !strings.HasPrefix(parts[1], "/api/") {
		return fmt.Errorf("invalid route pattern %q", pattern)
	}
	return routePatternParse(pattern)
}

func routePatternParse(pattern string) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("invalid route pattern %q", pattern)
		}
	}()
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	http.NewServeMux().Handle(pattern, h)
	return nil
}

func validateRoutePolicy(policy string, registry []byte) error {
	switch policy {
	case "public", "authenticated", "admin":
		return nil
	}
	name, ok := strings.CutPrefix(policy, "permission:")
	if !ok || name == "" {
		return fmt.Errorf("invalid route policy %q", policy)
	}
	c, err := config.Parse(registry)
	if err != nil {
		return fmt.Errorf("existing registry: %w", err)
	}
	if _, ok := c.Permissions[name]; !ok {
		return fmt.Errorf("route policy references unknown permission %q", name)
	}
	return nil
}

func routeSource(name, pattern, policy string) []byte {
	return []byte(fmt.Sprintf("package %s\n\nimport (\n\t\"net/http\"\n\n\t\"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx\"\n\t\"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web\"\n)\n\nfunc Register(reg *web.Registry) {\n\treg.Handle(%q, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {\n\t\thttpx.Fail(w, http.StatusNotImplemented, \"not_implemented\", \"Route is not implemented\")\n\t}), web.%s)\n}\n", name, pattern, routePolicyExpr(policy)))
}

func routePolicyExpr(policy string) string {
	switch policy {
	case "public":
		return "Public()"
	case "authenticated":
		return "Authenticated()"
	case "admin":
		return "Admin()"
	}
	return fmt.Sprintf("Permission(%q)", strings.TrimPrefix(policy, "permission:"))
}

// addRouteRegistration inserts one feature import and call, then applies gofmt.
func addRouteRegistration(source []byte, name string) ([]byte, error) {
	if err := validateName("route", name); err != nil || !token.IsIdentifier(name) || name == "main" {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("invalid route name %q", name)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "register.go", source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse register.go: %w", err)
	}
	if f.Name.Name != "app" {
		return nil, fmt.Errorf("register.go must declare package app")
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if x, ok := d.(*ast.FuncDecl); ok && x.Name.Name == "Register" {
			if fn != nil {
				return nil, fmt.Errorf("register.go has duplicate Register functions")
			}
			fn = x
		}
	}
	if fn == nil || fn.Recv != nil || fn.Type.TypeParams != nil || fn.Type.Results != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 || fn.Body == nil {
		return nil, fmt.Errorf("Register must be func Register(namedParam *web.Registry)")
	}
	param := fn.Type.Params.List[0].Names[0].Name
	if param == "_" {
		return nil, fmt.Errorf("Register parameter must have a usable name")
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return nil, fmt.Errorf("Register parameter must be *web.Registry")
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Registry" {
		return nil, fmt.Errorf("Register parameter must be *web.Registry")
	}
	webAlias, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, fmt.Errorf("Register parameter must use a web import")
	}
	imports := map[string]string{}
	var webPath string
	featurePath := "github.com/Profreshor/Dealership-Data-Platform/internal/app/" + name
	for _, d := range f.Imports {
		p, err := strconv.Unquote(d.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid import path: %w", err)
		}
		if p == featurePath {
			return nil, fmt.Errorf("feature %s is already imported", name)
		}
		local := p[strings.LastIndex(p, "/")+1:]
		if d.Name != nil {
			local = d.Name.Name
		}
		if local == "_" || local == "." {
			continue
		}
		imports[local] = p
		if p == "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web" {
			webPath = p
		}
	}
	if webPath == "" || imports[webAlias.Name] != webPath {
		return nil, fmt.Errorf("Register must use the web import")
	}
	alias := "route_" + name
	path := featurePath
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			used[id.Name] = true
		}
		return true
	})
	if used[alias] {
		return nil, fmt.Errorf("generated import alias %s is already used", alias)
	}
	if p, ok := imports[alias]; ok {
		return nil, fmt.Errorf("import alias %s already used for %s", alias, p)
	}
	for _, s := range fn.Body.List {
		e, ok := s.(*ast.ExprStmt)
		if !ok {
			return nil, fmt.Errorf("Register body may contain only direct feature.Register calls")
		}
		c, ok := e.X.(*ast.CallExpr)
		if !ok || c.Ellipsis.IsValid() || len(c.Args) != 1 || !sameIdent(c.Args[0], param) {
			return nil, fmt.Errorf("Register body may contain only direct feature.Register calls")
		}
		q, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return nil, fmt.Errorf("Register body may contain only direct feature.Register calls")
		}
		qx, qok := q.X.(*ast.Ident)
		if !qok || imports[qx.Name] == "" || q.Sel.Name != "Register" {
			return nil, fmt.Errorf("Register body may contain only direct feature.Register calls")
		}
	}
	last := f.Imports[len(f.Imports)-1]
	decl := importDeclFor(f, last)
	off := fset.Position(decl.Pos()).Offset
	if decl.Doc != nil {
		off = fset.Position(decl.Doc.Pos()).Offset
	}
	source = insert(source, fset.Position(fn.Body.Rbrace).Offset, []byte("\n\t"+alias+".Register("+param+")\n"))
	source = insert(source, off, []byte(fmt.Sprintf("import %s %q\n", alias, path)))
	return format.Source(source)
}

func insert(b []byte, at int, x []byte) []byte {
	return append(append(append([]byte(nil), b[:at]...), x...), b[at:]...)
}
func sameIdent(e ast.Expr, name string) bool { x, ok := e.(*ast.Ident); return ok && x.Name == name }
func importDeclFor(f *ast.File, s *ast.ImportSpec) *ast.GenDecl {
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			for _, x := range g.Specs {
				if x == s {
					return g
				}
			}
		}
	}
	return nil
}
