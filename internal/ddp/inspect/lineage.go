package inspect

import (
	"sort"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/registry"
)

// Lineage contains the dataflow resources before and after a registry resource.
type Lineage struct {
	Upstream   []string `json:"upstream"`
	Downstream []string `json:"downstream"`
}

// ResourceLineage follows declared dataflow relationships in both directions.
func ResourceLineage(reg *registry.Registry, ref string) Lineage {
	if reg == nil {
		return Lineage{Upstream: []string{}, Downstream: []string{}}
	}

	upstream := map[string][]string{}
	downstream := map[string][]string{}
	add := func(from, to string) {
		downstream[from] = append(downstream[from], to)
		upstream[to] = append(upstream[to], from)
	}
	for _, relationship := range reg.Relationships {
		switch relationship.Kind {
		case "reads":
			add(relationship.To, relationship.From)
		case "after", "endpoint", "target":
			add(relationship.To, relationship.From)
		case "writes", "model":
			add(relationship.From, relationship.To)
		}
	}

	return Lineage{
		Upstream:   walk(upstream, ref),
		Downstream: walk(downstream, ref),
	}
}

func walk(graph map[string][]string, start string) []string {
	visited := map[string]bool{start: true}
	queue := []string{start}
	result := []string{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range graph[current] {
			if visited[next] {
				continue
			}
			visited[next] = true
			queue = append(queue, next)
			result = append(result, next)
		}
	}
	sort.Strings(result)
	return result
}
