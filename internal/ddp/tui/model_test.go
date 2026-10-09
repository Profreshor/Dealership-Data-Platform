package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/charmbracelet/x/ansi"
)

func TestNavigationRejectsStaleFactsAndRequiresTypedAction(t *testing.T) {
	m := &model{ctx: t.Context(), cfg: &config.Config{Jobs: map[string]config.Job{"send": {Action: "notify", Idempotency: &config.Idempotency{Strategy: "natural_key"}}}}, width: 80, height: 24, generation: 1}
	m.Update(loaded{generation: 1, snapshot: Snapshot{Rows: []Row{{Ref: "job/send", JobRef: "job/send"}}}})
	m.Update(tea.KeyPressMsg{Code: 'x'})
	if m.confirmRef != "job/send" || m.confirmation != "natural_key" {
		t.Fatalf("missing side effect confirmation: %+v", m)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || m.running {
		t.Fatal("unconfirmed action ran")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.confirmRef != "" {
		t.Fatal("Esc failed to cancel confirmation")
	}
	m.snapshot.Rows = []Row{{Ref: jobs.CommsRelayRef, JobRef: jobs.CommsRelayRef}}
	m.Update(tea.KeyPressMsg{Code: 'x'})
	if m.confirmation != "duplicates_acceptable" {
		t.Fatal("platform email job lost its CLI strategy confirmation")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(tea.KeyPressMsg{Code: '4'})
	defer m.cancelLoad()
	if screens[m.screen] != Runs || len(m.snapshot.Rows) != 0 {
		t.Fatal("screen change retained actionable rows from another view")
	}
	m.Update(loaded{generation: 1, snapshot: Snapshot{Rows: []Row{{Ref: "stale"}}}})
	if len(m.snapshot.Rows) != 0 {
		t.Fatal("stale response replaced current screen")
	}
	if got := jobArgs("a registry.yaml", "job/send", "natural_key"); !reflect.DeepEqual(got, []string{"--config", "a registry.yaml", "jobs", "run", "job/send", "--json", "--confirm-idempotency", "natural_key"}) {
		t.Fatalf("command changed authority/confirmation: %q", got)
	}
	if got := jobArgs("ddp.yaml", "ddp:cleanup", "RUN"); len(got) != 6 {
		t.Fatalf("ordinary run added a strategy: %q", got)
	}
}

func TestTerminalContentCannotExecuteControlsOrOverflow(t *testing.T) {
	payload := "hello\x1b]52;c;Y2xpcGJvYXJk\a\x1b[2Jworld\r\u202esecret\x00"
	if got := clean(payload); strings.ContainsAny(got, "\x1b\a\r\x00\u202e") || !strings.Contains(got, "world") {
		t.Fatalf("unsafe cleanup: %q", got)
	}
	m := &model{ctx: context.Background(), width: 60, height: 18, snapshot: Snapshot{Rows: []Row{{Ref: payload, Label: strings.Repeat("wide界", 100)}}}}
	view := m.View()
	if !view.AltScreen {
		t.Fatal("terminal screen not isolated")
	}
	lines := strings.Split(view.Content, "\n")
	if len(lines) > m.height {
		t.Fatal("view overflowed terminal height")
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > m.width {
			t.Fatalf("view overflowed terminal width: %q", line)
		}
	}
	m.detailMode = "logs"
	m.detail = strings.Repeat("line\n", 100)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.offset == 0 {
		t.Fatal("log viewer did not scroll")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 100})
	if strings.Count(m.View().Content, "\n") >= m.height {
		t.Fatal("resized detail overflowed")
	}
}
