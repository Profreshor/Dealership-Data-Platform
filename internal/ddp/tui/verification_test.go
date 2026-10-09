package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

// A refresh keeps the old snapshot visible until its response arrives. That
// snapshot must not remain actionable while the replacement is loading.
func TestActionCannotStartWhileLoading(t *testing.T) {
	m := &model{
		ctx:      context.Background(),
		cfg:      &config.Config{Jobs: map[string]config.Job{"old": {Action: "check"}}},
		loading:  true,
		snapshot: Snapshot{Rows: []Row{{Ref: "job/old", JobRef: "job/old"}}},
	}
	m.Update(tea.KeyPressMsg{Code: 'x'})
	if m.confirmRef != "" || m.running {
		t.Fatalf("loading snapshot remained actionable: confirmation=%q running=%v", m.confirmRef, m.running)
	}
}
