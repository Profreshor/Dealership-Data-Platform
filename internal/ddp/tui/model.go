package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/charmbracelet/x/ansi"
	"github.com/jackc/pgx/v5/pgxpool"
)

type loaded struct {
	generation int
	snapshot   Snapshot
	detail     string
	err        error
}
type tick struct{}
type ran struct {
	output string
	err    error
}

type model struct {
	ctx                                   context.Context
	pool                                  *pgxpool.Pool
	cfg                                   *config.Config
	executable, registry                  string
	screen                                int
	width, height, cursor, offset         int
	snapshot                              Snapshot
	detail, detailRef, detailMode, notice string
	updated                               time.Time
	generation                            int
	loading, running                      bool
	cancelLoad                            context.CancelFunc
	confirmRef, confirmation, typed       string
}

// Run owns terminal state only. Data comes from inspection packages; mutations
// execute this binary's concrete jobs run command with its usual database role.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, executable, registry string, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := &model{ctx: ctx, pool: pool, cfg: cfg, executable: executable, registry: registry, width: 80, height: 24}
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output)).Run()
	return err
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.load(), nextTick()) }
func nextTick() tea.Cmd        { return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tick{} }) }
func (m *model) load() tea.Cmd {
	if m.cancelLoad != nil {
		m.cancelLoad()
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	m.cancelLoad = cancel
	m.generation++
	generation, screen, ref, mode := m.generation, screens[m.screen], m.detailRef, m.detailMode
	m.loading = true
	return func() tea.Msg {
		defer cancel()
		result := loaded{generation: generation}
		if mode != "" {
			result.detail, result.err = Detail(ctx, m.pool, m.cfg, ref, mode)
		} else {
			result.snapshot, result.err = Load(ctx, m.pool, m.cfg, screen)
		}
		return result
	}
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case loaded:
		if msg.generation != m.generation {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.notice = "Refresh failed: " + msg.err.Error() + " (previous observation retained)"
			return m, nil
		}
		m.notice = ""
		m.updated = time.Now()
		if m.detailMode != "" {
			m.detail = msg.detail
		} else {
			selected := m.selected().Ref
			m.snapshot = msg.snapshot
			m.cursor = min(m.cursor, max(0, len(m.snapshot.Rows)-1))
			for i, row := range m.snapshot.Rows {
				if row.Ref == selected {
					m.cursor = i
					break
				}
			}
		}
	case tick:
		if !m.loading && !m.running && m.confirmRef == "" && m.detailMode != "result" {
			return m, tea.Batch(m.load(), nextTick())
		}
		return m, nextTick()
	case ran:
		m.running = false
		m.detail = msg.output
		if msg.err != nil {
			m.detail = "Job command failed: " + msg.err.Error() + "\n\n" + msg.output
		} else {
			m.detail = "Job command completed.\n\n" + msg.output
		}
		m.detailMode = "result"
		m.offset = 0
		m.notice = "Esc returns to the list; job history keeps its execution and audit."
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.running {
			if key == "q" {
				return m, tea.Quit
			}
			return m, nil
		}
		if m.confirmRef != "" {
			switch key {
			case "esc":
				m.confirmRef, m.typed, m.confirmation = "", "", ""
			case "backspace":
				if len(m.typed) > 0 {
					m.typed = m.typed[:len(m.typed)-1]
				}
			case "enter":
				if m.typed == m.confirmation {
					return m, m.runJob()
				}
			default:
				if len(key) == 1 && key[0] >= 32 && key[0] <= 126 && len(m.typed) < 128 {
					m.typed += key
				}
			}
			return m, nil
		}
		if key == "q" {
			return m, tea.Quit
		}
		if key == "esc" {
			m.detailMode, m.detailRef, m.detail = "", "", ""
			m.offset = 0
			return m, m.load()
		}
		if key == "r" && m.detailMode != "result" {
			return m, m.load()
		}
		if m.detailMode != "" {
			lines := strings.Split(ansi.Hardwrap(clean(m.detail), m.width-2, true), "\n")
			step := max(1, m.height-9)
			switch key {
			case "up", "k":
				m.offset--
			case "down", "j":
				m.offset++
			case "pgup":
				m.offset -= step
			case "pgdown", "space":
				m.offset += step
			case "home":
				m.offset = 0
			case "end":
				m.offset = len(lines) - step
			}
			m.offset = max(0, min(m.offset, max(0, len(lines)-step)))
			return m, nil
		}
		switch key {
		case "tab", "right":
			return m, m.selectScreen((m.screen + 1) % len(screens))
		case "shift+tab", "left":
			return m, m.selectScreen((m.screen + len(screens) - 1) % len(screens))
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(max(0, len(m.snapshot.Rows)-1), m.cursor+1)
		case "pgup":
			m.cursor = max(0, m.cursor-max(1, m.height-12))
		case "pgdown":
			m.cursor = min(max(0, len(m.snapshot.Rows)-1), m.cursor+max(1, m.height-12))
		case "home":
			m.cursor = 0
		case "end":
			m.cursor = max(0, len(m.snapshot.Rows)-1)
		case "enter", "i", "d", "l":
			row := m.selected()
			if row.Ref == "" {
				return m, nil
			}
			mode := "inspect"
			if key == "d" {
				mode = "diagnose"
			}
			if key == "l" || (screens[m.screen] == Logs && key == "enter") {
				mode = "logs"
			}
			m.detailMode, m.detailRef, m.detail = mode, row.Ref, ""
			m.offset = 0
			return m, m.load()
		case "x":
			if m.loading {
				return m, nil
			}
			if row := m.selected(); row.JobRef != "" {
				m.confirmRef, m.confirmation, m.typed = row.JobRef, "RUN", ""
				if job, ok := jobs.Definitions(m.cfg)[row.JobRef]; ok && job.SideEffecting() && job.Idempotency != nil {
					m.confirmation = job.Idempotency.Strategy
				}
			}
		default:
			if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
				return m, m.selectScreen((int(key[0]-'0') + 9) % 10)
			}
		}
	}
	return m, nil
}

func (m *model) selectScreen(screen int) tea.Cmd {
	m.screen, m.cursor, m.offset = screen, 0, 0
	m.snapshot = Snapshot{}
	m.updated = time.Time{}
	m.notice = ""
	return m.load()
}

func (m *model) selected() Row {
	if m.cursor < 0 || m.cursor >= len(m.snapshot.Rows) {
		return Row{}
	}
	return m.snapshot.Rows[m.cursor]
}
func (m *model) runJob() tea.Cmd {
	ref, confirmation := m.confirmRef, m.typed
	m.confirmRef, m.typed, m.confirmation = "", "", ""
	m.running = true
	m.notice = "Running " + ref + ". Ctrl+C cancels through the CLI."
	if m.cancelLoad != nil {
		m.cancelLoad()
		m.generation++
		m.loading = false
	}
	ctx, cancel := context.WithCancel(m.ctx)
	command := exec.CommandContext(ctx, m.executable, jobArgs(m.registry, ref, confirmation)...)
	// Allow the CLI signal handler to stop its owned workload and persist outcome.
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 15 * time.Second
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	return tea.ExecProcess(command, func(err error) tea.Msg { cancel(); return ran{output.String(), err} })
}
func jobArgs(registry, ref, confirmation string) []string {
	args := []string{"--config", registry, "jobs", "run", ref, "--json"}
	if confirmation != "RUN" {
		args = append(args, "--confirm-idempotency", confirmation)
	}
	return args
}

// Strip terminal commands and remaining controls from stored logs and metadata.
func clean(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '\uFFFD'
		}
		return r
	}, ansi.Strip(value))
}
func line(value string) string { return strings.ReplaceAll(clean(value), "\n", " ") }
func (m *model) View() tea.View {
	if m.width < 60 || m.height < 18 {
		view := tea.NewView(ansi.Truncate("Resize terminal to 60×18 or larger; q quits.", max(0, m.width-1), ""))
		view.AltScreen = true
		return view
	}
	var lines []string
	title := "DDP  /  " + string(screens[m.screen])
	if m.detailMode != "" {
		title += "  /  " + m.detailMode
	}
	lines = append(lines, title)
	nav := ""
	for i, screen := range screens {
		label := fmt.Sprintf("%d %s", (i+1)%10, screen)
		if i == m.screen {
			label = "[" + label + "]"
		}
		nav += label + "  "
	}
	lines = append(lines, ansi.Hardwrap(nav, m.width-2, true))
	observed := "No observation yet"
	if !m.updated.IsZero() {
		observed = "Fetched " + m.updated.UTC().Format("15:04:05 UTC")
	}
	if m.loading {
		observed += " | loading"
	}
	lines = append(lines, observed)
	if m.notice != "" {
		lines = append(lines, line(m.notice))
	}
	lines = append(lines, "")
	if m.confirmRef != "" {
		lines = append(lines, "Run "+line(m.confirmRef)+"?", "This starts a new execution with your current database privileges.", "Type "+line(m.confirmation)+" and press Enter; Esc cancels.", "> "+line(m.typed))
	} else if m.detailMode != "" {
		detail := strings.Split(ansi.Hardwrap(clean(m.detail), m.width-2, true), "\n")
		count := max(1, m.height-strings.Count(strings.Join(lines, "\n"), "\n")-4)
		start := min(m.offset, max(0, len(detail)-count))
		lines = append(lines, detail[start:min(len(detail), start+count)]...)
	} else {
		for _, summary := range m.snapshot.Summary {
			lines = append(lines, line(summary))
		}
		count := max(1, m.height-strings.Count(strings.Join(lines, "\n"), "\n")-5)
		start := max(0, m.cursor-count+1)
		for i := start; i < min(len(m.snapshot.Rows), start+count); i++ {
			row := m.snapshot.Rows[i]
			marker := "  "
			if i == m.cursor {
				marker = "> "
			}
			lines = append(lines, marker+line(row.Ref)+"  "+line(row.State)+"  "+line(row.Label))
		}
		if len(m.snapshot.Rows) == 0 && !m.loading {
			lines = append(lines, "No records.")
		}
	}
	lines = append(lines, "", "Tab / 0–9 screen  ↑↓ select  Enter inspect  d diagnose  l logs", "x run job  r refresh  Esc back  q quit")
	// Bound both dimensions even on small terminals or unusually long metadata.
	var visible []string
	for _, item := range lines {
		for _, part := range strings.Split(item, "\n") {
			visible = append(visible, ansi.Truncate(part, m.width-1, "…"))
		}
	}
	view := tea.NewView(strings.Join(visible[:min(len(visible), m.height)], "\n"))
	view.AltScreen = true
	return view
}
