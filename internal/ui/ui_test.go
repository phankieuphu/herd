package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/core"
	"github.com/phankieuphu/herd/internal/limits"
	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/tmux"
)

func testModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("HERD_HOME", t.TempDir())
	cfg := config.NewStore(config.Default())
	eng := &core.Engine{T: &tmux.Client{Bin: "tmux", Socket: "herd-ui-test-unused"}, Cfg: cfg, Self: "/bin/true"}
	m := New(eng, cfg)
	m.width, m.height = 34, 40
	now := time.Now().Unix()
	snap := &core.Snapshot{
		Main:   "herd",
		Groups: []string{"api"},
		Folded: map[string]bool{},
		Panes: []model.Pane{
			{ID: "%0", Session: "herd", Role: model.RoleSidebar, Width: 34},
			{ID: "%1", Session: "herd-fix-auth", Kind: "claude", Title: "fix auth", Home: "herd-fix-auth", Cmd: "claude",
				Branch: "herd/fix-auth", View: model.NeedsYou, ViewTS: now - 120, Read: false, Created: now - 600, Hooked: true, HookState: model.NeedsYou},
			{ID: "%2", Session: "herd-docs", Kind: "codex", Title: "docs", Home: "herd-docs", Cmd: "codex", Group: "api",
				View: model.Working, Read: true, Created: now - 600},
			{ID: "%3", Session: "herd-logs", Kind: "shell", Title: "logs", Home: "herd-logs", Cmd: "bash", Group: "api",
				View: model.Done, Read: true, Inactive: true, Created: now - 600},
			{ID: "%4", Session: "herd", Role: model.RolePlaceholder},
		},
	}
	screens := map[string]string{
		"%1": "Do you want to proceed?\n❯ 1. Yes\n",
		"%2": "• Working (3s • esc to interrupt)\n",
		"%3": "$ ",
	}
	m.applySnapshot(snap, screens)
	return m
}

func plain(m Model) string { return ansi.Strip(m.View()) }

func TestViewRows(t *testing.T) {
	m := testModel(t)
	v := plain(m)
	for _, want := range []string{"herd", "◆ fix auth •", "2m", "claude · herd/fix-auth", "▾ api 2", "docs", "☾ logs", "◆1", "n new"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	for i, l := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(l); w != m.width {
			t.Errorf("line %d has width %d, want %d: %q", i, w, m.width, ansi.Strip(l))
		}
	}
	if n := len(strings.Split(m.View(), "\n")); n != m.height {
		t.Errorf("view has %d lines, want %d", n, m.height)
	}
	// preview of the selected agent (not on stage)
	if !strings.Contains(v, "── fix auth") || !strings.Contains(v, "Do you want to proceed?") {
		t.Errorf("no preview:\n%s", v)
	}
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}
	return m
}

func TestKeys(t *testing.T) {
	m := testModel(t)
	if sel := m.selectedAgent(); sel == nil || sel.ID != "%1" {
		t.Fatalf("first selection %+v", sel)
	}
	m = press(m, "j")
	if r := m.selected(); r == nil || !r.header || r.group != "api" {
		t.Fatalf("j should land on the group header: %+v", r)
	}
	m = press(m, "j", "j", "j", "j")
	if sel := m.selectedAgent(); sel == nil || sel.ID != "%3" {
		t.Fatalf("cursor should stop at the last row: %+v", sel)
	}
	m = press(m, "k", "k")
	if sel := m.selectedAgent(); sel != nil {
		t.Fatalf("expected header, got %+v", sel)
	}

	// approve is refused when the agent does not need you
	m = press(m, "j", "a")
	if !strings.Contains(m.msg, "nothing to answer") {
		t.Fatalf("msg %q", m.msg)
	}

	// new agent dialog
	m = press(m, "n")
	if m.dlg == nil || m.dlg.kind != dlgNew {
		t.Fatal("n should open the new dialog")
	}
	if !strings.Contains(plain(m), "‹ claude ›") {
		t.Fatalf("dialog:\n%s", plain(m))
	}
	if m.dlg.target != "api" {
		t.Fatalf("new agent should default to the selected group, got %q", m.dlg.target)
	}
	m = press(m, "tab", "x", "y")
	if m.dlg.value(1) != "xy" {
		t.Fatalf("name field: %q", m.dlg.value(1))
	}
	m = press(m, "esc")
	if m.dlg != nil {
		t.Fatal("esc should close")
	}

	// filter
	m = press(m, "/", "l", "o", "g")
	if len(m.rows) != 1 || m.rows[0].agent.ID != "%3" {
		t.Fatalf("filter rows %+v", m.rows)
	}
	m = press(m, "esc")
	if len(m.rows) != 4 {
		t.Fatalf("filter cleared rows %d", len(m.rows))
	}

	// help
	m = press(m, "?")
	if !strings.Contains(plain(m), "herd keys") {
		t.Fatal("help")
	}
	m = press(m, "x")
	if m.dlg != nil {
		t.Fatal("any key closes help")
	}

	// tab jumps to the agent that needs you
	m = press(m, "k", "k", "k", "j", "j")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if m.selKey != "%1" || cmd == nil {
		t.Fatalf("tab: sel %s", m.selKey)
	}
}

func TestLimitLines(t *testing.T) {
	m := testModel(t)
	m.limits = []limits.Provider{
		{Key: "claude", Name: "Claude", Five: limits.Window{Used: 42, Valid: true}, Week: limits.Window{Used: 85, Valid: true}},
		{Key: "codex", Name: "codex", Stale: true},
		{Key: "x", Name: "x", Five: limits.Window{Used: 100, Valid: true}},
	}
	lines := m.limitLines()
	got := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"Claude 5h ▰▰▱▱▱ 42% wk ▰▰▰▰▱ 85%", "codex  ╌╌ stale", "x      5h ▰▰▰▰▰ ●   wk ╌╌╌╌╌  --"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("too wide (%d): %q", w, ansi.Strip(l))
		}
	}
}

func TestEmptyView(t *testing.T) {
	t.Setenv("HERD_HOME", t.TempDir())
	cfg := config.NewStore(config.Default())
	m := New(&core.Engine{T: &tmux.Client{Bin: "tmux", Socket: "x"}, Cfg: cfg}, cfg)
	m.width, m.height = 34, 20
	if !strings.Contains(plain(m), "loading") {
		t.Fatal("loading")
	}
	m.applySnapshot(&core.Snapshot{Main: "herd", Folded: map[string]bool{}}, nil)
	if !strings.Contains(plain(m), "No agents yet") {
		t.Fatalf("%s", plain(m))
	}
}

func TestFuzzy(t *testing.T) {
	if !fuzzy("fix auth herd/fix-auth", "fxa") || fuzzy("docs", "dz") || !fuzzy("Tiếng Việt", "tv") {
		t.Fatal("fuzzy")
	}
}
