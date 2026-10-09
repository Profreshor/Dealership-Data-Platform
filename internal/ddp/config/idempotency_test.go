package config

import (
	"strings"
	"testing"
)

func TestSideEffectingJobsRequireExplicitIdempotency(t *testing.T) {
	for _, action := range []string{"notify", "export", "operate"} {
		cfg, err := Load("../testdata/reporting/ddp.yaml")
		if err != nil {
			t.Fatal(err)
		}
		job := cfg.Jobs["sync_customers"]
		job.Action = action
		cfg.Jobs["sync_customers"] = job
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires an idempotency strategy") {
			t.Fatalf("unsafe %s accepted: %v", action, err)
		}
		for _, strategy := range []string{"provider_key", "natural_key", "reconcile", "duplicates_acceptable"} {
			job.Idempotency = &Idempotency{Strategy: strategy}
			if strategy == "duplicates_acceptable" {
				if err := job.ValidateIdempotency(); err == nil {
					t.Fatal("duplicate effects accepted without reason")
				}
				job.Idempotency.Reason = "Synthetic test messages may repeat."
			}
			cfg.Jobs["sync_customers"] = job
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid %s rejected: %v", strategy, err)
			}
		}
	}
	if err := (Job{Idempotency: &Idempotency{Strategy: "once"}}).ValidateIdempotency(); err == nil {
		t.Fatal("unknown strategy accepted")
	}
}
