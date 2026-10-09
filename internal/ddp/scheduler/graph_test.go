package scheduler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestChain(t *testing.T) {
	tests := []struct {
		name     string
		jobs     map[string]config.Job
		root     string
		want     []string
		contains string
	}{
		{name: "linear", jobs: chainJobs("a", "b", "c"), root: "job/a", want: []string{"job/a", "job/b", "job/c"}},
		{name: "diamond", jobs: map[string]config.Job{
			"root": {}, "left": {After: []string{"job/root"}}, "right": {After: []string{"job/root"}}, "join": {After: []string{"job/right", "job/left"}},
		}, root: "job/root", want: []string{"job/root", "job/left", "job/right", "job/join"}},
		{name: "unrelated jobs", jobs: map[string]config.Job{"a": {}, "b": {}, "child": {After: []string{"job/a"}}}, root: "job/a", want: []string{"job/a", "job/child"}},
		{name: "manual root ignores its parent", jobs: map[string]config.Job{"prior": {}, "root": {After: []string{"job/prior"}}, "child": {After: []string{"job/root"}}}, root: "job/root", want: []string{"job/root", "job/child"}},
		{name: "unknown", jobs: map[string]config.Job{"a": {After: []string{"job/missing"}}}, root: "job/a", contains: "unknown parent"},
		{name: "cycle", jobs: map[string]config.Job{"a": {After: []string{"job/b"}}, "b": {After: []string{"job/a"}}}, root: "job/a", contains: "cycle"},
		{name: "outside scheduled root", jobs: map[string]config.Job{"a": {Schedule: "* * * * *"}, "b": {Schedule: "* * * * *"}, "join": {After: []string{"job/a", "job/b"}}}, root: "job/a", contains: "outside chain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Chain(&config.Config{Jobs: tt.jobs}, tt.root)
			if tt.contains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.contains) {
					t.Fatalf("error %v, want %q", err, tt.contains)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, err %v; want %v", got, err, tt.want)
			}
		})
	}
	if _, err := Chain(nil, "job/a"); err == nil {
		t.Fatal("nil config accepted")
	}
}

func chainJobs(names ...string) map[string]config.Job {
	result := map[string]config.Job{}
	for i, name := range names {
		var after []string
		if i > 0 {
			after = []string{"job/" + names[i-1]}
		}
		result[name] = config.Job{After: after}
	}
	return result
}
