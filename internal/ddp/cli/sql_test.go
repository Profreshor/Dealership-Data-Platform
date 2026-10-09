package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/query"
)

func TestSQLValidationHappensBeforeDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-real-database")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"SELECT 1", "--confirm", "abc"}, 2},
		{[]string{"SELECT 1", "--write"}, 4},
		{[]string{"SELECT 1", "--write", "--confirm", "wrong"}, 4},
		{[]string{"SELECT 1", "--limit", "0"}, 2},
		{[]string{"SELECT 1", "--limit", "10001"}, 2},
		{[]string{"SELECT 1", "--timeout", "0s"}, 2},
		{[]string{"SELECT 1", "--timeout", "6m"}, 2},
		{[]string{""}, 2}, {[]string{"SELECT\x00 1"}, 2},
		{[]string{strings.Repeat("x", 256*1024+1)}, 2},
		{[]string{"SELECT 1", "--write", "--confirm", query.Fingerprint("SELECT 1")}, 1},
	} {
		var out, logs bytes.Buffer
		code := Execute(t.Context(), append(append([]string{"sql"}, tc.args...), "--json"), &out, &logs, nil)
		var envelope struct {
			OK    bool
			Error struct{ Message string }
		}
		if code != tc.code || json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.OK || logs.Len() != 0 {
			t.Fatalf("code=%d want=%d out=%s logs=%s", code, tc.code, &out, &logs)
		}
		if tc.code != 1 && strings.Contains(envelope.Error.Message, "connect to database") {
			t.Fatal("input validation occurred after connecting")
		}
	}
	t.Setenv("DATABASE_URL", "")
	var out, logs bytes.Buffer
	if code := Execute(t.Context(), []string{"sql", "SELECT 1", "--json"}, &out, &logs, nil); code != 1 || !strings.Contains(out.String(), "DATABASE_URL is required") {
		t.Fatalf("missing database: %d %s", code, &out)
	}
}
