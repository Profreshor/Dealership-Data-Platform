package platform

import "testing"

func TestDeploymentWithoutDatabaseIsUnknown(t *testing.T) {
	got := Deployment(t.Context(), nil)
	if got == nil || got.State != "unknown" || got.Severity != "critical" || got.Message == "" {
		t.Fatalf("deployment without database: %+v", got)
	}
}
