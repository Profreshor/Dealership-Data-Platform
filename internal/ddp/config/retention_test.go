package config

import "testing"

func TestRetentionDefaultsAndValidation(t *testing.T) {
	cfg, err := Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := Retention{RunLogs: "720h", Health: "2160h", Messages: "720h"}
	if got := cfg.RetentionPolicy(); got != want {
		t.Fatalf("defaults %+v", got)
	}
	cfg.Retention = &Retention{Messages: "48h"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.RetentionPolicy(); got.Messages != "48h" || got.RunLogs != want.RunLogs || got.Health != want.Health {
		t.Fatalf("partial policy %+v", got)
	}
	for _, bad := range []string{"0s", "-1h", "30d", "not-a-duration"} {
		cfg.Retention = &Retention{RunLogs: bad}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid retention %q accepted", bad)
		}
	}
}
