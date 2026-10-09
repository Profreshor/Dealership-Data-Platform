package onboard

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oklog/ulid/v2"
)

// accountMigration seeds only identities discovered by the client. Permissions
// remain an explicit operator decision during bootstrap and administration.
func accountMigration(d Discovery) string {
	roles := make(map[string]struct{}, len(d.Users))
	for _, user := range d.Users {
		roles[user.Role] = struct{}{}
	}
	roleIDs := make([]string, 0, len(roles))
	for role := range roles {
		roleIDs = append(roleIDs, role)
	}
	sort.Strings(roleIDs)
	var out strings.Builder
	out.WriteString("-- Discovered pending accounts; permissions are intentionally empty.\n")
	for _, role := range roleIDs {
		fmt.Fprintf(&out, "INSERT INTO app.roles(id,name) VALUES (%s,%s);\n", sqlString(role), sqlString(role))
	}
	for _, user := range d.Users {
		id := ulid.Make().String()
		email := strings.ToLower(strings.TrimSpace(user.Email))
		fmt.Fprintf(&out, "INSERT INTO app.users(id,email,password_hash,is_admin) VALUES (%s,%s,NULL,false);\n", sqlString(id), sqlString(email))
		fmt.Fprintf(&out, "INSERT INTO app.user_roles(user_id,role_id) VALUES (%s,%s);\n", sqlString(id), sqlString(user.Role))
	}
	return out.String()
}

func sqlString(value string) string {
	return "E'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `\'`) + "'"
}
