package inspect

import (
	"context"
	"testing"
)

func TestResourceInspectionRejectsMalformedReferences(t *testing.T) {
	for _, ref := range []string{"table/core", "table/core.a.b", "model/", "integration/a/b"} {
		if _, _, err := relationRef(ref); err == nil && ref != "integration/a/b" {
			t.Fatalf("accepted malformed relation %q", ref)
		}
	}
	if _, err := namedRef("integration/a/b", "integration"); err == nil {
		t.Fatal("accepted malformed integration")
	}
}

func TestResourceInspectionRequiresInputs(t *testing.T) {
	ctx := context.Background()
	if _, err := ListTables(ctx, nil, nil); err == nil {
		t.Fatal("accepted missing inspection inputs")
	}
	if _, err := ListIntegrations(ctx, nil, nil); err == nil {
		t.Fatal("accepted missing inspection inputs")
	}
}
