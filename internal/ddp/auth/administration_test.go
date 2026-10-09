package auth

import (
	"context"
	"strings"
	"testing"
)

func TestAdministrationRejectsInvalidInputBeforeDatabase(t *testing.T) {
	s := New(nil, 0)
	if err := s.UpdateAccount(context.Background(), "", nil, false, ""); err != ErrInvalidAccount {
		t.Fatalf("UpdateAccount error = %v", err)
	}
	if err := s.UpdateAccount(context.Background(), "user", []string{strings.Repeat("r", 129)}, false, ""); err != ErrInvalidAccount {
		t.Fatalf("UpdateAccount role error = %v", err)
	}
	if err := s.SaveRole(context.Background(), nil, "role", "name", []string{""}, ""); err != ErrInvalidAccount {
		t.Fatalf("SaveRole permission error = %v", err)
	}
	if err := s.SaveRole(context.Background(), nil, "role", "", nil, ""); err != ErrInvalidAccount {
		t.Fatalf("SaveRole name error = %v", err)
	}
	for _, ref := range []string{"", "  ", strings.Repeat("u", 255)} {
		if _, err := s.DisableAccount(context.Background(), ref, ""); err != ErrInvalidAccount {
			t.Fatalf("DisableAccount(%q) error = %v", ref, err)
		}
	}
	if _, err := s.DisableAccount(context.Background(), "user", strings.Repeat("c", 255)); err != ErrInvalidAccount {
		t.Fatalf("DisableAccount confirm error = %v", err)
	}
}
