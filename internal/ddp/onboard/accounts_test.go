package onboard

import (
	"strings"
	"testing"
)

func TestAccountMigrationSortsRolesAndEscapesLiterals(t *testing.T) {
	d := Discovery{Users: []User{{Email: "A@EXAMPLE.COM", Role: "zeta"}, {Email: "b@example.com", Role: "alpha"}}}
	sql := accountMigration(d)
	if strings.Index(sql, "'alpha'") > strings.Index(sql, "'zeta'") {
		t.Fatalf("roles are not sorted: %s", sql)
	}
	if !strings.Contains(sql, "password_hash,is_admin) VALUES") || !strings.Contains(sql, "NULL,false") {
		t.Fatalf("accounts are not pending: %s", sql)
	}
	if !strings.Contains(sql, "'a@example.com'") {
		t.Fatalf("email was not normalized: %s", sql)
	}
	if got := sqlString(`O'Reilly\sync`); got != `E'O\'Reilly\\sync'` {
		t.Fatalf("literal escaping = %q", got)
	}
}
