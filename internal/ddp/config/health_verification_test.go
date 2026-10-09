package config

import "testing"

func TestHealthDeclarationValidation(t *testing.T) {
	load := func(t *testing.T) *Config {
		t.Helper()
		cfg, err := Load("../testdata/base/ddp.yaml")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Tables["core.events"] = Table{Contract: Contract{Columns: map[string]Column{
			"observed_at": {Type: "timestamptz"},
			"label":       {Type: "text"},
		}}}
		return cfg
	}

	valid := []Health{
		{Kind: "freshness", Target: "table/core.events", Column: "observed_at", MaxAge: "1h", Severity: "warning"},
		{Kind: "sql", SQL: "health/events.sql", Severity: "critical"},
	}
	for _, health := range valid {
		cfg := load(t)
		cfg.Health["events"] = health
		if err := cfg.Validate(); err != nil {
			t.Fatalf("rejected valid declaration %#v: %v", health, err)
		}
	}

	invalid := []Health{
		{Kind: "unknown", Severity: "warning"},
		{Kind: "freshness", Column: "observed_at", MaxAge: "1h", Severity: "warning"},
		{Kind: "freshness", Target: "table/core.events", MaxAge: "1h", Severity: "warning"},
		{Kind: "freshness", Target: "table/core.events", Column: "observed_at", MaxAge: "0s", Severity: "warning"},
		{Kind: "freshness", Target: "table/core.events", Column: "missing", MaxAge: "1h", Severity: "warning"},
		{Kind: "freshness", Target: "table/core.events", Column: "label", MaxAge: "1h", Severity: "warning"},
		{Kind: "freshness", Target: "table/core.events", Column: "observed_at", MaxAge: "1h", SQL: "health/events.sql", Severity: "warning"},
		{Kind: "sql", Severity: "warning"},
		{Kind: "sql", SQL: "health/events.sql", Column: "observed_at", Severity: "warning"},
		{Kind: "sql", SQL: "health/events.sql", MaxAge: "1h", Severity: "warning"},
	}
	for _, health := range invalid {
		cfg := load(t)
		cfg.Health["events"] = health
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted invalid declaration %#v", health)
		}
	}
}
