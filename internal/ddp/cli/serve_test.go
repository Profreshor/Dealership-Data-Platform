package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeRequiresSeparateCredentialsAndKeepsErrorsSafe(t *testing.T) {
	for _, tc := range []struct{ api, scheduler string }{
		{"", ""},
		{"postgres://private-value", ""},
		{"", "postgres://private-value"},
		{"postgres://user:private-value@host:badport/db", "invalid private-value"},
	} {
		t.Setenv("DATABASE_URL", tc.api)
		t.Setenv("SCHEDULER_DATABASE_URL", tc.scheduler)
		var out, logs bytes.Buffer
		code := Execute(t.Context(), []string{"serve", "--all", "--json", "--config", "../testdata/base/ddp.yaml", "--api-metrics-addr", "127.0.0.1:0", "--scheduler-metrics-addr", "127.0.0.1:0"}, &out, &logs, nil)
		var envelope struct {
			Version int
			OK      bool
			Error   struct{ Message string }
		}
		if code != 1 || json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.OK || envelope.Version != 1 || envelope.Error.Message == "" || strings.Contains(out.String(), "private-value") || logs.Len() != 0 {
			t.Fatalf("code=%d output=%s logs=%s", code, &out, &logs)
		}
	}
}
