package scaffold

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"go.yaml.in/yaml/v3"
)

type Request struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Definition []byte `json:"definition"`
	Source     []byte `json:"source"`
}

type Proposal struct {
	Kind     string            `json:"kind"`
	Ref      string            `json:"ref"`
	Registry []byte            `json:"registry"`
	Files    map[string][]byte `json:"files"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var relationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
var migrationPattern = regexp.MustCompile(`^[0-9]{14}_[a-z][a-z0-9_]*$`)

func Build(existingRegistry []byte, request Request) (Proposal, error) {
	if _, err := config.Parse(existingRegistry); err != nil {
		return Proposal{}, fmt.Errorf("existing registry: %w", err)
	}
	root, err := parseDocument(existingRegistry, "registry")
	if err != nil {
		return Proposal{}, err
	}
	if request.Kind == "route" {
		return buildRoute(existingRegistry, request)
	}
	if request.Kind != "migration" && request.Kind != "job" && request.Kind != "model" && request.Kind != "integration" && request.Kind != "endpoint" && request.Kind != "page" && request.Kind != "health" {
		return Proposal{}, fmt.Errorf("unknown scaffold kind %q", request.Kind)
	}
	if request.Kind == "migration" {
		if len(bytes.TrimSpace(request.Definition)) > 0 {
			return Proposal{}, fmt.Errorf("migration definition is unsupported; migration SQL is the authority")
		}
		return buildMigration(existingRegistry, request)
	}
	if strings.TrimSpace(string(request.Definition)) == "" {
		return Proposal{}, fmt.Errorf("%s definition is required", request.Kind)
	}
	if err := validateName(request.Kind, request.Name); err != nil {
		return Proposal{}, err
	}
	value, err := parseValue(request.Definition, "definition")
	if err != nil {
		return Proposal{}, err
	}
	if value.Kind != yaml.MappingNode {
		return Proposal{}, fmt.Errorf("%s definition must be a YAML mapping", request.Kind)
	}
	if err := rejectAliases(value); err != nil {
		return Proposal{}, err
	}
	section := sectionFor(request.Kind)
	if err := appendSection(root, section, request.Name, value); err != nil {
		return Proposal{}, err
	}
	files := map[string][]byte{}
	if err := addArtifacts(request, value, files); err != nil {
		return Proposal{}, err
	}
	registry, err := marshalDocument(root)
	if err != nil {
		return Proposal{}, err
	}
	if _, err := config.Parse(registry); err != nil {
		return Proposal{}, fmt.Errorf("proposed registry: %w", err)
	}
	return Proposal{Kind: request.Kind, Ref: request.Kind + "/" + request.Name, Registry: registry, Files: files}, nil
}

func buildMigration(registry []byte, r Request) (Proposal, error) {
	if !migrationPattern.MatchString(r.Name) {
		return Proposal{}, fmt.Errorf("invalid migration name %q: use YYYYMMDDHHMMSS_slug", r.Name)
	}
	if _, err := time.Parse("20060102150405", r.Name[:14]); err != nil {
		return Proposal{}, fmt.Errorf("invalid migration timestamp: %w", err)
	}
	if len(bytes.TrimSpace(r.Source)) == 0 {
		return Proposal{}, fmt.Errorf("migration source is required")
	}
	return Proposal{Kind: r.Kind, Ref: "migration/" + r.Name, Registry: append([]byte(nil), registry...), Files: map[string][]byte{"migrations/app/" + r.Name + ".sql": append([]byte(nil), r.Source...)}}, nil
}

func validateName(kind, name string) error {
	if strings.ContainsAny(name, `/\\`) || strings.TrimSpace(name) != name || name == "" {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	if (kind == "model" && !relationPattern.MatchString(name)) || (kind != "model" && !namePattern.MatchString(name)) {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	return nil
}

func addArtifacts(r Request, value *yaml.Node, files map[string][]byte) error {
	if r.Source != nil && len(bytes.TrimSpace(r.Source)) == 0 {
		return fmt.Errorf("source must not be empty")
	}
	if r.Kind == "page" {
		kind, err := mappingString(value, "kind")
		if err != nil {
			return err
		}
		if kind == "custom" {
			label, err := mappingString(value, "label")
			if err != nil {
				return err
			}
			labelJSON, _ := json.Marshal(label)
			idJSON, _ := json.Marshal(r.Name)
			files["frontend/apps/portal/src/routes/"+r.Name+".tsx"] = []byte(fmt.Sprintf(`import { registerPage } from "@ddp/ui";

function Page() {
  // Implement the client workflow with the shared API client and authorized endpoints.
  return <section aria-labelledby="page-title">
    <div className="page-heading"><div><h1 id="page-title">{%s}</h1><p>This page is not ready yet.</p></div></div>
  </section>;
}

registerPage(%s, Page);
`, labelJSON, idJSON))
		}
	}
	source := append([]byte(nil), r.Source...)
	switch r.Kind {
	case "job":
		model, err := mappingString(value, "model")
		if err != nil {
			return err
		}
		if model != "" {
			if len(source) > 0 {
				return fmt.Errorf("model job cannot include Python source")
			}
			return nil
		}
		if err := ensureMappingString(value, "python", "jobs."+r.Name); err != nil {
			return err
		}
		if len(source) == 0 {
			source = []byte("from ddp import JobContext, JobResult, job\n\n\n@job\ndef run(ctx: JobContext) -> JobResult:\n    raise NotImplementedError\n")
		}
		files["jobs/"+r.Name+".py"] = source
	case "model":
		if len(bytes.TrimSpace(source)) == 0 {
			return fmt.Errorf("model SQL source is required")
		}
		path := "models/" + strings.ReplaceAll(r.Name, ".", "/") + ".sql"
		if err := ensureMappingString(value, "file", path); err != nil {
			return err
		}
		files[path] = source
	case "integration":
		if len(source) == 0 {
			source = []byte("\"\"\"Client integration: " + r.Name + ".\"\"\"\n")
		}
		files["client/"+r.Name+".py"] = source
	case "health":
		kind, err := mappingString(value, "kind")
		if err != nil {
			return err
		}
		if len(source) > 0 {
			if kind != "sql" {
				return fmt.Errorf("health source requires kind sql")
			}
			path := "health/" + r.Name + ".sql"
			if err := ensureMappingString(value, "sql", path); err != nil {
				return err
			}
			files[path] = source
		} else if kind == "sql" {
			return fmt.Errorf("health kind sql requires non-empty SQL source")
		}
	case "endpoint", "page":
		if len(source) > 0 {
			return fmt.Errorf("%s does not accept source", r.Kind)
		}
	}
	return nil
}

func sectionFor(kind string) string {
	return map[string]string{"job": "jobs", "model": "models", "integration": "integrations", "endpoint": "endpoints", "page": "pages", "health": "health"}[kind]
}

func appendSection(root *yaml.Node, section, name string, value *yaml.Node) error {
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("registry must be a YAML mapping")
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != section {
			continue
		}
		m := root.Content[i+1]
		if m.Kind != yaml.MappingNode {
			return fmt.Errorf("registry section %s must be a mapping", section)
		}
		for j := 0; j < len(m.Content); j += 2 {
			if m.Content[j].Value == name {
				return fmt.Errorf("duplicate %s/%s", section, name)
			}
		}
		// Empty sections in the template use flow style ({}); new entries should
		// remain readable block YAML while retaining the section's position.
		if len(m.Content) == 0 {
			m.Style = 0
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, value)
		return nil
	}
	return fmt.Errorf("registry missing top-level section %s", section)
}

func parseDocument(b []byte, what string) (*yaml.Node, error) {
	var n yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&n); err != nil {
		return nil, fmt.Errorf("decode %s: %w", what, err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%s must contain one YAML document", what)
	}
	if n.Kind != yaml.DocumentNode || len(n.Content) != 1 {
		return nil, fmt.Errorf("%s must contain one YAML document", what)
	}
	return n.Content[0], nil
}

func parseValue(b []byte, what string) (*yaml.Node, error) {
	n, err := parseDocument(b, what)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func marshalDocument(n *yaml.Node) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{n}}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func rejectAliases(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("YAML aliases are unsupported")
	}
	for _, child := range n.Content {
		if err := rejectAliases(child); err != nil {
			return err
		}
	}
	return nil
}

func mappingString(n *yaml.Node, key string) (string, error) {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				return "", fmt.Errorf("definition field %s must be a string", key)
			}
			return v.Value, nil
		}
	}
	return "", nil
}
func ensureMappingString(n *yaml.Node, key, value string) error {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				return fmt.Errorf("definition field %s must be a string", key)
			}
			if v.Value != value {
				return fmt.Errorf("definition field %s must be %q", key, value)
			}
			return nil
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return nil
}
