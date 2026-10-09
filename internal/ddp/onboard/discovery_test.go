package onboard

import (
	"strings"
	"testing"
)

const validDiscovery = `client:
  name: acme
  industry: dealership
  timezone: America/Chicago
  locations: []
  contacts:
    - name: Owner
      email: owner@example.com
users:
  - name: Analyst
    email: analyst@example.com
    role: analyst
systems: []
questions:
  - Which jobs are most profitable?
outputs:
  pages: []
  emails: []
communications:
  groups: []
hosting:
  mode: cloud
  domain: app.example.com
  exposure: cloudflare
  repository: https://github.com/acme/portal
ownership:
  repository: operator
  host: dealership
  domain: dealership
  cloudflare: operator
  backups: operator
  integration_credentials: dealership
recovery:
  backup_interval: 24h
  backup_retention: 14d
  max_data_loss: 24h
  target_restore_time: 2h
constraints:
  pii: []
  data_retention: Keep records for the contracted period.
  development_data: synthetic
success_30_days:
  - Owner can answer the profitability question.
`

func TestParseDiscovery(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{name: "valid", data: validDiscovery},
		{name: "unknown field", data: strings.Replace(validDiscovery, "systems: []", "systems: []\nunknown: true", 1), wantErr: true},
		{name: "duplicate key", data: strings.Replace(validDiscovery, "client:\n", "client:\n  name: other\n", 1), wantErr: true},
		{name: "alias", data: strings.Replace(validDiscovery, "systems: []", "systems: &systems []\nquestions: *systems", 1), wantErr: true},
		{name: "multiple documents", data: validDiscovery + "\n---\n" + validDiscovery, wantErr: true},
		{name: "bad role", data: strings.Replace(validDiscovery, "role: analyst", "role: Analyst", 1), wantErr: true},
		{name: "duplicate user email", data: strings.Replace(validDiscovery, "users:\n  - name: Analyst", "users:\n  - name: Analyst\n    email: analyst@example.com\n    role: analyst\n  - name: Second", 1), wantErr: true},
		{name: "bad duration", data: strings.Replace(validDiscovery, "backup_interval: 24h", "backup_interval: 1x", 1), wantErr: true},
		{name: "retention too short", data: strings.Replace(validDiscovery, "backup_retention: 14d", "backup_retention: 1h", 1), wantErr: true},
		{name: "bad domain", data: strings.Replace(validDiscovery, "domain: app.example.com", "domain: https://app.example.com", 1), wantErr: true},
		{name: "bad ownership", data: strings.Replace(validDiscovery, "backups: operator", "backups: vendor", 1), wantErr: true},
		{name: "bad repository", data: strings.Replace(validDiscovery, "https://github.com/acme/portal", "https://gitlab.com/acme/portal", 1), wantErr: true},
		{name: "missing sequence", data: strings.Replace(validDiscovery, "systems: []\n", "", 1), wantErr: true},
		{name: "oversized", data: validDiscovery + strings.Repeat("# padding\n", (1<<20)/9), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseDiscovery([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseDiscovery error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
