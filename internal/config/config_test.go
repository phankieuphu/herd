package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Sidebar.Session != "herd" || c.Sidebar.Width != 34 || c.Sidebar.Poll.Duration != time.Second {
		t.Fatalf("sidebar %+v", c.Sidebar)
	}
	if c.Defaults.Agent != "claude" || !c.Defaults.Worktree {
		t.Fatalf("defaults %+v", c.Defaults)
	}
	if len(c.Agents["claude"].NeedsYou) == 0 || c.Agents["codex"].Command != "codex" {
		t.Fatal("built-in agents missing")
	}
	if got := c.AgentNames(); got[0] != "claude" || got[1] != "codex" || got[2] != "shell" {
		t.Fatalf("names %v", got)
	}
}

func TestOverride(t *testing.T) {
	c, err := Parse(`
[sidebar]
width = 40
poll = "500ms"

[agents.claude]
command = "claude --model opus"

[agents.gemini]
command = "gemini"
working = ["esc to cancel"]

[defaults]
agent = "gemini"
worktree = false

[presets.review]
agent = "claude"
prompt = "review"

[integrations.limit_rings]
enabled = true
file = "~/l.json"

[notify.push]
enabled = true
provider = "telegram"
away_after = "5m"
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sidebar.Width != 40 || c.Sidebar.Poll.Duration != 500*time.Millisecond {
		t.Fatalf("sidebar %+v", c.Sidebar)
	}
	cl := c.Agents["claude"]
	if cl.Command != "claude --model opus" || len(cl.NeedsYou) == 0 || len(cl.ApproveKeys) == 0 {
		t.Fatalf("claude merge lost built-in rules: %+v", cl)
	}
	if c.Agents["gemini"].Command != "gemini" || c.Defaults.Agent != "gemini" || c.Defaults.Worktree {
		t.Fatal("gemini / defaults")
	}
	if c.Agents["codex"].Command != "codex" {
		t.Fatal("codex should still exist")
	}
	if c.Presets["review"].Prompt != "review" || c.PresetNames()[0] != "review" {
		t.Fatal("presets")
	}
	if !c.Integrations.LimitRings.Enabled || c.Notify.Push.AwayAfter.Duration != 5*time.Minute || c.Notify.Push.Provider != "telegram" {
		t.Fatal("integrations / push")
	}
	if !c.Integrations.Tmux.BindKeys || c.Integrations.Tmux.FocusKey != "prefix C-h" {
		t.Fatal("bind_keys default lost")
	}
}

func TestKeySpec(t *testing.T) {
	for s, want := range map[string]string{
		"prefix C-h": "prefix C-h", "M-h": "root M-h", "  M-Left ": "root M-Left",
		"": "", "prefix": "", "a b": "", "prefix C-h x": "",
	} {
		table, key, ok := KeySpec(s)
		got := ""
		if ok {
			got = table + " " + key
		}
		if got != want {
			t.Errorf("KeySpec(%q) = %q, want %q", s, got, want)
		}
	}
	c, err := Parse("[integrations.tmux]\nfocus_key = \"M-h\"\njump_key = \"\"\n")
	if err != nil || c.Integrations.Tmux.FocusKey != "M-h" || c.Integrations.Tmux.JumpKey != "" {
		t.Fatalf("keys: %+v %v", c.Integrations.Tmux, err)
	}
}

func TestInvalid(t *testing.T) {
	for _, s := range []string{
		`[defaults]
agent = "nope"`,
		`[presets.x]
agent = "nope"`,
		`[sidebar]
session = "a.b"`,
		`[notify.push]
provider = "sms"`,
		`[sidebar]
poll = "soon"`,
		`[integrations.tmux]
focus_key = "ctrl h"`,
		`not toml`,
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("want error for %q", s)
		}
	}
}

func TestClamp(t *testing.T) {
	c, err := Parse("[sidebar]\nwidth = 5\npoll = \"1ms\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Sidebar.Width != 20 || c.Sidebar.Poll.Duration != 200*time.Millisecond {
		t.Fatalf("%+v", c.Sidebar)
	}
}

func TestExampleParses(t *testing.T) {
	if _, err := Parse(Example); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndWatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERD_HOME", dir)
	c, err := Load()
	if err != nil || c.Sidebar.Width != 34 {
		t.Fatal("missing file should give defaults", err)
	}
	got := make(chan *Config, 4)
	stop, err := Watch(func(c *Config, err error) {
		if err == nil {
			got <- c
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := os.WriteFile(filepath.Join(dir, "settings.toml"), []byte("[sidebar]\nwidth = 50\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-got:
		if c.Sidebar.Width != 50 {
			t.Fatalf("width %d", c.Sidebar.Width)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reload")
	}
}

func TestExpand(t *testing.T) {
	home, _ := os.UserHomeDir()
	if Expand("~/x") != filepath.Join(home, "x") || Expand("/a/~") != "/a/~" {
		t.Fatal("expand")
	}
}
