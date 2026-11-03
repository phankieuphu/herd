package ui

import (
	"os"
	"path/filepath"
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
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
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
	m = press(m, "down", "x", "y")
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

func TestFolderComplete(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"code/api", "code/apps", "code/web", "Documents", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "code", "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := press(testModel(t), "n")
	in := &m.dlg.inputs[0]
	in.SetValue(root + "/co")
	m = press(m, "tab")
	if got := m.dlg.inputs[0].Value(); got != root+"/code/" {
		t.Fatalf("one match: %q", got)
	}
	m = press(m, "a")
	if got := m.dlg.inputs[0].Value(); got != root+"/code/a" {
		t.Fatalf("typed: %q", got)
	}
	if strings.Join(m.dlg.sugg, ",") != "api,apps" {
		t.Fatalf("suggestions %v", m.dlg.sugg)
	}
	m = press(m, "tab") // common prefix
	if got := m.dlg.inputs[0].Value(); got != root+"/code/ap" {
		t.Fatalf("prefix: %q", got)
	}
	m = press(m, "tab", "tab") // cycle
	if got := m.dlg.inputs[0].Value(); got != root+"/code/apps/" {
		t.Fatalf("cycle: %q", got)
	}
	if v := plain(m); !strings.Contains(v, "api/") || !strings.Contains(v, "tab complete") {
		t.Errorf("view:\n%s", v)
	}
	m.dlg.inputs[0].SetValue(root + "/d")
	m = press(m, "tab") // case-insensitive, hidden folders skipped
	if got := m.dlg.inputs[0].Value(); got != root+"/Documents/" {
		t.Fatalf("case: %q", got)
	}
	m.dlg.inputs[0].SetValue(root + "/nothing")
	m = press(m, "tab") // no match: next field
	if m.dlg.focus != 2 {
		t.Fatalf("focus %d", m.dlg.focus)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillComplete(t *testing.T) {
	user, proj := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", user)
	writeFile(t, filepath.Join(user, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review the diff\n---\n")
	writeFile(t, filepath.Join(user, "skills", "refactor", "SKILL.md"), "")
	writeFile(t, filepath.Join(user, "commands", "release.md"), "")
	writeFile(t, filepath.Join(proj, ".claude", "skills", "deploy", "SKILL.md"), "")

	m := press(testModel(t), "n")
	m.dlg.inputs[0].SetValue(proj)
	m = press(m, "down", "down") // prompt field
	if m.dlg.focus != 3 {
		t.Fatalf("focus %d", m.dlg.focus)
	}
	m = press(m, "/", "d", "tab") // one match, project skill
	if got := m.dlg.inputs[2].Value(); got != "/deploy " {
		t.Fatalf("one match: %q", got)
	}
	m.dlg.inputs[2].SetValue("")
	m = press(m, "/", "r")
	if len(m.dlg.skillSugg) != 3 {
		t.Fatalf("suggestions %v", m.dlg.skillSugg)
	}
	if v := plain(m); !strings.Contains(v, "/review") || !strings.Contains(v, "Review the") || !strings.Contains(v, "tab complete") {
		t.Errorf("view:\n%s", v)
	}
	m = press(m, "tab") // common prefix "/re"
	if got := m.dlg.inputs[2].Value(); got != "/re" {
		t.Fatalf("prefix: %q", got)
	}
	m = press(m, "tab", "tab") // cycle: refactor, release
	if got := m.dlg.inputs[2].Value(); got != "/release" {
		t.Fatalf("cycle: %q", got)
	}
	m = press(m, "shift+tab")
	if got := m.dlg.inputs[2].Value(); got != "/refactor" {
		t.Fatalf("back: %q", got)
	}
	m.dlg.inputs[2].SetValue("fix /re")
	m.dlg.inputs[2].CursorEnd()
	m = press(m, "tab") // "/" only counts as the first word: next field
	if m.dlg.focus != 4 {
		t.Fatalf("focus %d", m.dlg.focus)
	}

	m = press(m, "esc", "m", "/", "r", "e", "v", "tab") // message to a claude agent
	if m.dlg.kind != dlgMessage || m.dlg.inputs[0].Value() != "/review " {
		t.Fatalf("message: %q", m.dlg.inputs[0].Value())
	}
}

func TestFindSkillsCodex(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeFile(t, filepath.Join(home, "prompts", "fix.md"), "---\ndescription: \"Fix it\"\n---\n")
	writeFile(t, filepath.Join(home, "skills", "lint", "SKILL.md"), "")
	writeFile(t, filepath.Join(proj, ".codex", "skills", "ship", "SKILL.md"), "")
	got := findSkills("codex", proj)
	var names []string
	for _, s := range got {
		names = append(names, s.name)
	}
	if strings.Join(names, ",") != "$lint,$ship,/prompts:fix" || got[2].desc != "Fix it" {
		t.Fatalf("%v", got)
	}
	if skillFlavor("shell", "bash") != "" || skillFlavor("work", "/usr/bin/claude --x") != "claude" {
		t.Fatal("flavor")
	}
}
