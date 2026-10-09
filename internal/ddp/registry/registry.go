// Package registry exposes the validated, declared project graph.
package registry

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

var types = []string{
	"endpoint", "group", "health", "integration", "job",
	"model", "page", "permission", "table",
}

type Registry struct {
	Types         []string              `json:"types"`
	Resources     []Resource            `json:"resources"`
	Relationships []config.Relationship `json:"relationships"`
	byRef         map[string]Resource
}

type Resource struct {
	Ref          string   `json:"ref"`
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Purpose      string   `json:"purpose"`
	Config       any      `json:"config"`
	Dependencies []string `json:"dependencies"`
	Dependents   []string `json:"dependents"`
	ReadBy       []string `json:"read_by"`
	WrittenBy    []string `json:"written_by"`
}

func Build(cfg *config.Config) (*Registry, error) {
	if cfg == nil {
		return nil, fmt.Errorf("registry: config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	relationships := cfg.Relationships()
	resources := declaredResources(cfg)
	byRef := make(map[string]Resource, len(resources))
	for _, resource := range resources {
		byRef[resource.Ref] = resource
	}
	for _, relationship := range relationships {
		from, fromOK := byRef[relationship.From]
		to, toOK := byRef[relationship.To]
		if !fromOK || !toOK {
			return nil, fmt.Errorf("registry: relationship %s -> %s references unknown resource", relationship.From, relationship.To)
		}
		from.Dependencies = append(from.Dependencies, relationship.To)
		to.Dependents = append(to.Dependents, relationship.From)
		if relationship.Kind == "reads" {
			to.ReadBy = append(to.ReadBy, relationship.From)
		}
		if relationship.Kind == "writes" {
			to.WrittenBy = append(to.WrittenBy, relationship.From)
		}
		byRef[from.Ref] = from
		byRef[to.Ref] = to
	}
	for i := range resources {
		resources[i] = byRef[resources[i].Ref]
		sort.Strings(resources[i].Dependencies)
		sort.Strings(resources[i].Dependents)
		sort.Strings(resources[i].ReadBy)
		sort.Strings(resources[i].WrittenBy)
		resources[i].Dependencies = slices.Compact(resources[i].Dependencies)
		resources[i].Dependents = slices.Compact(resources[i].Dependents)
		resources[i].ReadBy = slices.Compact(resources[i].ReadBy)
		resources[i].WrittenBy = slices.Compact(resources[i].WrittenBy)
		byRef[resources[i].Ref] = resources[i]
	}
	return &Registry{Types: append([]string{}, types...), Resources: resources, Relationships: relationships, byRef: byRef}, nil
}

func (r *Registry) Get(ref string) (Resource, error) {
	if r == nil {
		return Resource{}, fmt.Errorf("registry resource %q not found", ref)
	}
	resource, ok := r.byRef[ref]
	if !ok {
		return Resource{}, fmt.Errorf("registry resource %q not found", ref)
	}
	return resource, nil
}

func (r *Registry) Search(term string) []Resource {
	if r == nil {
		return nil
	}
	term = strings.ToLower(term)
	result := make([]Resource, 0)
	for _, resource := range r.Resources {
		configText, _ := json.Marshal(resource.Config)
		if strings.Contains(strings.ToLower(resource.Ref), term) ||
			strings.Contains(strings.ToLower(resource.Name), term) ||
			strings.Contains(strings.ToLower(resource.Purpose), term) ||
			strings.Contains(strings.ToLower(string(configText)), term) {
			result = append(result, resource)
		}
	}
	return result
}

func declaredResources(cfg *config.Config) []Resource {
	resources := make([]Resource, 0)
	for name, value := range cfg.Integrations {
		resources = append(resources, resource("integration/"+name, "integration", name, "", value))
	}
	for name, value := range cfg.Jobs {
		resources = append(resources, resource("job/"+name, "job", name, value.Purpose, value))
	}
	for name, value := range cfg.Tables {
		resources = append(resources, resource("table/"+name, "table", name, value.Purpose, value))
	}
	for name, value := range cfg.Models {
		resources = append(resources, resource("model/"+name, "model", name, value.Purpose, value))
	}
	for name, value := range cfg.Endpoints {
		resources = append(resources, resource("endpoint/"+name, "endpoint", name, "", value))
	}
	for name, value := range cfg.Pages {
		resources = append(resources, resource("page/"+name, "page", name, value.Label, value))
	}
	for name, value := range cfg.Health {
		resources = append(resources, resource("health/"+name, "health", name, "", value))
	}
	for name, value := range cfg.Comms.Groups {
		resources = append(resources, resource("group/"+name, "group", name, "", value))
	}
	for name, value := range cfg.Permissions {
		resources = append(resources, resource("permission/"+name, "permission", name, value.Description, value))
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Ref < resources[j].Ref })
	return resources
}

func resource(ref, kind, name, purpose string, value any) Resource {
	return Resource{
		Ref: ref, Type: kind, Name: name, Purpose: purpose, Config: value,
		Dependencies: []string{}, Dependents: []string{}, ReadBy: []string{}, WrittenBy: []string{},
	}
}
