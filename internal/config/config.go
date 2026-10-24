// Package config loads ~/.herd/settings.toml and reloads it when it changes.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
)

// Duration is a time.Duration that reads "2m", "500ms" from TOML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Agent describes how to start and read one kind of agent.
type Agent struct {
	Command     string   `toml:"command"`
	NeedsYou    []string `toml:"needs_you"`
	Working     []string `toml:"working"`
	ApproveKeys []string `toml:"approve_keys"`
	DenyKeys    []string `toml:"deny_keys"`
}

// Preset is a saved way to start an agent.
type Preset struct {
	Agent    string `toml:"agent"`
	Dir      string `toml:"dir"`
	Name     string `toml:"name"`
	Prompt   string `toml:"prompt"`
	Group    string `toml:"group"`
	Worktree *bool  `toml:"worktree"`
}

type Sidebar struct {
	Session      string   `toml:"session"`
	Width        int      `toml:"width"`
	Poll         Duration `toml:"poll"`
	Preview      bool     `toml:"preview"`
	PreviewLines int      `toml:"preview_lines"`
	Compact      bool     `toml:"compact"`
}

type LimitRings struct {
	Enabled bool   `toml:"enabled"`
	File    string `toml:"file"`
}

type TmuxIntegration struct {
	SessionScript string `toml:"session_script"`
	BindKeys      bool   `toml:"bind_keys"`
}

type Integrations struct {
	LimitRings LimitRings      `toml:"limit_rings"`
	Tmux       TmuxIntegration `toml:"tmux"`
}

type Defaults struct {
	Agent        string `toml:"agent"`
	Worktree     bool   `toml:"worktree"`
	WorktreeDir  string `toml:"worktree_dir"`
	BranchPrefix string `toml:"branch_prefix"`
}

type Push struct {
	Enabled        bool     `toml:"enabled"`
	Provider       string   `toml:"provider"` // ntfy | telegram
	NtfyURL        string   `toml:"ntfy_url"`
	NtfyToken      string   `toml:"ntfy_token"`
	TelegramToken  string   `toml:"telegram_token"`
	TelegramChatID string   `toml:"telegram_chat_id"`
	AwayAfter      Duration `toml:"away_after"`
}

type Notify struct {
	Desktop bool     `toml:"desktop"`
	Tmux    bool     `toml:"tmux"`
	On      []string `toml:"on"` // needs_you, done, exited
	Push    Push     `toml:"push"`
}

// Config is the whole settings file.
type Config struct {
	Sidebar      Sidebar           `toml:"sidebar"`
	Integrations Integrations      `toml:"integrations"`
	Defaults     Defaults          `toml:"defaults"`
	Agents       map[string]Agent  `toml:"agents"`
	Presets      map[string]Preset `toml:"presets"`
	Notify       Notify            `toml:"notify"`
}

// BuiltinAgents are the agent kinds herd knows out of the box.
func BuiltinAgents() map[string]Agent {
	return map[string]Agent{
		"claude": {
			Command: "claude",
			NeedsYou: []string{
				`Do you want to (proceed|make this edit|create|allow|run|overwrite|delete)`,
				`❯\s*1\.\s*Yes`,
				`Would you like to proceed`,
				`\bpermission\b.*\?\s*$`,
			},
			Working:     []string{`esc to interrupt`, `ctrl\+c to interrupt`, `\(\d+s ·.*tokens`},
			ApproveKeys: []string{"Enter"},
			DenyKeys:    []string{"Escape"},
		},
		"codex": {
			Command: "codex",
			NeedsYou: []string{
				`Would you like to (run|make|apply)`,
				`Allow command\?`,
				`Yes, proceed`,
				`\[y/N\]|\(y/n\)`,
				`Approve\b.*\?`,
			},
			Working:     []string{`esc to interrupt`, `Working \(`, `Esc to interrupt`},
			ApproveKeys: []string{"y"},
			DenyKeys:    []string{"Escape"},
		},
		"shell": {Command: ""},
	}
}

// Default returns the built-in settings.
func Default() *Config {
	return &Config{
		Sidebar: Sidebar{Session: "herd", Width: 34, Poll: Duration{time.Second}, Preview: true, PreviewLines: 8},
		Integrations: Integrations{
			Tmux: TmuxIntegration{BindKeys: true},
		},
		Defaults: Defaults{Agent: "claude", Worktree: true, BranchPrefix: "herd/"},
		Agents:   BuiltinAgents(),
		Presets:  map[string]Preset{},
		Notify: Notify{
			Desktop: true, Tmux: true, On: []string{"needs_you", "done"},
			Push: Push{Provider: "ntfy", AwayAfter: Duration{2 * time.Minute}},
		},
	}
}

// Dir is the herd home folder: $HERD_HOME or ~/.herd.
func Dir() string {
	if d := os.Getenv("HERD_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".herd"
	}
	return filepath.Join(home, ".herd")
}

// WorktreeRoot is where new worktrees go: defaults.worktree_dir, or
// <herd home>/worktrees.
func (c *Config) WorktreeRoot() string {
	if c.Defaults.WorktreeDir != "" {
		return Expand(c.Defaults.WorktreeDir)
	}
	return filepath.Join(Dir(), "worktrees")
}

// Path is the settings file path.
func Path() string { return filepath.Join(Dir(), "settings.toml") }

// Expand replaces a leading ~ with the home folder.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// Parse reads settings from TOML text on top of the defaults.
func Parse(data string) (*Config, error) {
	c := Default()
	c.Agents = nil
	if _, err := toml.Decode(data, c); err != nil {
		return nil, err
	}
	// Merge agents: user values win field by field over built-ins.
	merged := BuiltinAgents()
	for name, a := range c.Agents {
		b, ok := merged[name]
		if !ok {
			merged[name] = a
			continue
		}
		if a.Command != "" {
			b.Command = a.Command
		}
		if a.NeedsYou != nil {
			b.NeedsYou = a.NeedsYou
		}
		if a.Working != nil {
			b.Working = a.Working
		}
		if a.ApproveKeys != nil {
			b.ApproveKeys = a.ApproveKeys
		}
		if a.DenyKeys != nil {
			b.DenyKeys = a.DenyKeys
		}
		merged[name] = b
	}
	c.Agents = merged
	if c.Presets == nil {
		c.Presets = map[string]Preset{}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.Sidebar.Width < 20 {
		c.Sidebar.Width = 20
	}
	if c.Sidebar.Poll.Duration < 200*time.Millisecond {
		c.Sidebar.Poll.Duration = 200 * time.Millisecond
	}
	if c.Sidebar.PreviewLines < 0 {
		c.Sidebar.PreviewLines = 0
	}
	if c.Sidebar.Session == "" {
		c.Sidebar.Session = "herd"
	}
	if strings.ContainsAny(c.Sidebar.Session, ".:") {
		return fmt.Errorf("sidebar.session %q: tmux session names cannot contain '.' or ':'", c.Sidebar.Session)
	}
	if _, ok := c.Agents[c.Defaults.Agent]; !ok {
		return fmt.Errorf("defaults.agent %q is not defined in [agents]", c.Defaults.Agent)
	}
	for name, p := range c.Presets {
		if p.Agent != "" {
			if _, ok := c.Agents[p.Agent]; !ok {
				return fmt.Errorf("presets.%s: unknown agent %q", name, p.Agent)
			}
		}
	}
	switch c.Notify.Push.Provider {
	case "", "ntfy", "telegram":
	default:
		return fmt.Errorf("notify.push.provider %q: use ntfy or telegram", c.Notify.Push.Provider)
	}
	return nil
}

// Load reads the settings file. A missing file gives the defaults.
func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Path(), err)
	}
	return c, nil
}

// AgentNames lists agent kinds in a stable order: built-ins first.
func (c *Config) AgentNames() []string {
	names := []string{}
	for _, b := range []string{"claude", "codex", "shell"} {
		if _, ok := c.Agents[b]; ok {
			names = append(names, b)
		}
	}
	var extra []string
	for n := range c.Agents {
		if n != "claude" && n != "codex" && n != "shell" {
			extra = append(extra, n)
		}
	}
	sortStrings(extra)
	return append(names, extra...)
}

// PresetNames lists presets sorted by name.
func (c *Config) PresetNames() []string {
	var out []string
	for n := range c.Presets {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

// NotifyOn reports whether notifications are wanted for a state name.
func (c *Config) NotifyOn(state string) bool {
	for _, s := range c.Notify.On {
		if s == state {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Store holds the current config and swaps it on reload.
type Store struct{ p atomic.Pointer[Config] }

func NewStore(c *Config) *Store { s := &Store{}; s.p.Store(c); return s }
func (s *Store) Get() *Config   { return s.p.Load() }
func (s *Store) Set(c *Config)  { s.p.Store(c) }

// Watch calls onChange with a fresh config (or an error) when the settings
// file changes. It returns a stop function.
func Watch(onChange func(*Config, error)) (func(), error) {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		var timer *time.Timer
		for {
			select {
			case <-done:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != "settings.toml" {
					continue
				}
				if timer != nil {
					timer.Stop()
				}
				// editors write in several steps; wait for them to settle
				timer = time.AfterFunc(150*time.Millisecond, func() {
					c, err := Load()
					onChange(c, err)
				})
			case <-w.Errors:
			}
		}
	}()
	return func() { close(done); w.Close() }, nil
}

// Example is a commented settings file written by `herd init-config`.
const Example = `# herd settings. herd reloads this file when it changes.

[sidebar]
# session = "herd"      # name of the main tmux session
# width = 34            # sidebar width in columns
# poll = "1s"           # how often the sidebar reads tmux
# preview = true        # show the last lines of the selected agent
# preview_lines = 8
# compact = false       # one line per agent instead of two

[defaults]
# agent = "claude"                  # claude | codex | shell | any [agents.X]
# worktree = true                   # new agents in a git repo get their own worktree
# worktree_dir = "~/.herd/worktrees"   # default: <herd home>/worktrees
# branch_prefix = "herd/"

[integrations.limit_rings]
# enabled = false
# file = "~/.cache/limit-rings.json"

[integrations.tmux]
# bind_keys = true        # prefix C-h = sidebar, prefix C-j = next agent that needs you
# session_script = ""     # rename/group script

[notify]
# desktop = true
# tmux = true
# on = ["needs_you", "done"]

[notify.push]
# enabled = false
# provider = "ntfy"                  # ntfy | telegram
# ntfy_url = "https://ntfy.sh/my-secret-topic"
# telegram_token = ""
# telegram_chat_id = ""
# away_after = "2m"                  # only push when no tmux client was active for this long

# Add your own agent. The rules are Go regular expressions matched
# against the visible screen of the pane.
# [agents.gemini]
# command = "gemini"
# working = ["esc to cancel"]
# needs_you = ["Allow execution\\?"]
# approve_keys = ["Enter"]
# deny_keys = ["Escape"]

# Saved ways to start an agent: press N in the sidebar or run
# herd new --preset review
# [presets.review]
# agent = "claude"
# dir = "~/code/api"
# prompt = "Review the last commit and list problems."
# group = "reviews"
# worktree = false
`
