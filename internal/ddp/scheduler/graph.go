package scheduler

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

// Chain returns the root and all of its downstream jobs in dependency order.
func Chain(cfg *config.Config, rootRef string) ([]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("scheduler chain: nil config")
	}
	if !strings.HasPrefix(rootRef, "job/") || rootRef == "job/" {
		return nil, fmt.Errorf("scheduler chain: invalid root %q", rootRef)
	}
	if _, ok := cfg.Jobs[strings.TrimPrefix(rootRef, "job/")]; !ok {
		return nil, fmt.Errorf("scheduler chain: unknown root %q", rootRef)
	}

	parents := make(map[string][]string, len(cfg.Jobs))
	children := make(map[string][]string, len(cfg.Jobs))
	refs := make([]string, 0, len(cfg.Jobs))
	for name := range cfg.Jobs {
		refs = append(refs, "job/"+name)
	}
	slices.Sort(refs)
	for _, ref := range refs {
		job := cfg.Jobs[strings.TrimPrefix(ref, "job/")]
		seen := map[string]bool{}
		for _, parent := range job.After {
			if !strings.HasPrefix(parent, "job/") || parent == "job/" {
				return nil, fmt.Errorf("%s has invalid parent %q", ref, parent)
			}
			if _, ok := cfg.Jobs[strings.TrimPrefix(parent, "job/")]; !ok {
				return nil, fmt.Errorf("%s references unknown parent %q", ref, parent)
			}
			if seen[parent] {
				continue
			}
			seen[parent] = true
			parents[ref] = append(parents[ref], parent)
			children[parent] = append(children[parent], ref)
		}
		slices.Sort(parents[ref])
		slices.Sort(children[ref])
	}

	if err := acyclic(refs, children); err != nil {
		return nil, err
	}

	chosen := map[string]bool{rootRef: true}
	stack := []string{rootRef}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, child := range children[current] {
			if !chosen[child] {
				chosen[child] = true
				stack = append(stack, child)
			}
		}
	}
	for ref := range chosen {
		if ref == rootRef {
			continue
		}
		for _, parent := range parents[ref] {
			if !chosen[parent] {
				return nil, fmt.Errorf("%s depends on %s outside chain rooted at %s", ref, parent, rootRef)
			}
		}
	}

	indegree := map[string]int{}
	for ref := range chosen {
		for _, parent := range parents[ref] {
			if chosen[parent] {
				indegree[ref]++
			}
		}
	}
	ready := []string{rootRef}
	order := make([]string, 0, len(chosen))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)
		for _, child := range children[current] {
			if !chosen[child] {
				continue
			}
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
		slices.Sort(ready)
	}
	if len(order) != len(chosen) {
		return nil, fmt.Errorf("scheduler chain rooted at %s contains a cycle", rootRef)
	}
	return order, nil
}

func acyclic(refs []string, children map[string][]string) error {
	indegree := make(map[string]int, len(refs))
	for _, ref := range refs {
		for _, child := range children[ref] {
			indegree[child]++
		}
	}
	ready := []string{}
	processed := 0
	for _, ref := range refs {
		if indegree[ref] == 0 {
			ready = append(ready, ref)
		}
	}
	for len(ready) > 0 {
		ref := ready[0]
		ready = ready[1:]
		processed++
		for _, child := range children[ref] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
		slices.Sort(ready)
	}
	if processed == len(refs) {
		return nil
	}
	for _, ref := range refs {
		if indegree[ref] > 0 {
			return fmt.Errorf("scheduler graph contains a cycle involving %s", ref)
		}
	}
	return nil
}
