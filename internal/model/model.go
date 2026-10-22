// Package model holds the data herd keeps about panes and agents. All of it
// lives in tmux pane options, so it survives restarts and every tmux client
// sees the same thing.
package model

import (
	"strconv"
	"strings"

	"github.com/phankieuphu/herd/internal/tmux"
)

// State is what an agent is doing right now.
type State string

const (
	Unknown    State = "unknown"
	Working    State = "working"
	NeedsYou   State = "needs_you"
	Done       State = "done"
	Background State = "background"
	Exited     State = "exited"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case Unknown, Working, NeedsYou, Done, Background, Exited:
		return true
	}
	return false
}

// Attention reports whether a state asks the user to look.
func (s State) Attention() bool { return s == NeedsYou || s == Done || s == Exited }

// Busy reports whether the agent is still doing work.
func (s State) Busy() bool { return s == Working || s == Background }

// Pane user option names.
const (
	OptRole       = "@herd_role" // sidebar | placeholder
	OptKind       = "@herd_agent"
	OptTitle      = "@herd_title"
	OptGroup      = "@herd_group"
	OptOrder      = "@herd_order"
	OptHome       = "@herd_home"
	OptDir        = "@herd_dir"
	OptRepo       = "@herd_repo"
	OptWorktree   = "@herd_worktree"
	OptBranch     = "@herd_branch"
	OptBase       = "@herd_base"
	OptInactive   = "@herd_inactive"
	OptRead       = "@herd_read"
	OptHooked     = "@herd_hooked"
	OptTranscript = "@herd_transcript"
	OptState      = "@herd_state"    // raw state from hooks
	OptStateTS    = "@herd_state_ts" // unix ms
	OptBG         = "@herd_bg"
	OptBGKinds    = "@herd_bg_kinds"
	OptView       = "@herd_view" // state shown in the sidebar
	OptViewTS     = "@herd_view_ts"
	OptCreated    = "@herd_created" // unix seconds

	// session options
	OptAdopt     = "@herd_adopt"
	OptWarpAdopt = "@warp_adopt"
	OptWarpGroup = "@warp_group"

	// global options
	OptGroups    = "@herd_groups"    // group order, "\n" separated
	OptCollapsed = "@herd_collapsed" // collapsed groups, "\n" separated
)

// RolePlaceholder marks the pane that keeps a slot when its agent is shown elsewhere.
const (
	RoleSidebar     = "sidebar"
	RolePlaceholder = "placeholder"
)

var paneVars = []string{
	"pane_id", "session_name", "session_id", "window_index", "pane_index",
	"pane_pid", "pane_current_command", "pane_current_path", "pane_dead",
	"pane_active", "window_active", "session_attached", "pane_width", "pane_height",
	OptRole, OptKind, OptTitle, OptGroup, OptOrder, OptHome, OptDir, OptRepo,
	OptWorktree, OptBranch, OptBase, OptInactive, OptRead, OptHooked,
	OptTranscript, OptState, OptStateTS, OptBG, OptBGKinds, OptView, OptViewTS,
	OptCreated, OptAdopt, OptWarpAdopt, OptWarpGroup,
}

// PaneFormat is the -F format used to list panes.
var PaneFormat = tmux.Format(paneVars...)

// Pane is one tmux pane with herd metadata.
type Pane struct {
	ID        string
	Session   string
	SessionID string
	Window    int
	Index     int
	PID       int
	Cmd       string
	Path      string
	Dead      bool
	Active    bool
	WinActive bool
	Attached  int
	Width     int
	Height    int

	Role       string
	Kind       string
	Title      string
	Group      string
	Order      float64
	Home       string
	Dir        string
	Repo       string
	Worktree   string
	Branch     string
	Base       string
	Inactive   bool
	Read       bool
	Hooked     bool
	Transcript string
	HookState  State
	HookTS     int64
	BG         int
	BGKinds    string
	View       State
	ViewTS     int64
	Created    int64

	AdoptFlag bool
	WarpGroup string
}

// IsAgent reports whether herd manages this pane as an agent.
func (p Pane) IsAgent() bool { return p.Kind != "" && p.Role == "" && !p.Dead }

// Name is what the user sees.
func (p Pane) Name() string {
	if p.Title != "" {
		return p.Title
	}
	return strings.TrimPrefix(p.Home, "herd-")
}

// ParsePanes parses list-panes output in PaneFormat.
func ParsePanes(out string) []Pane {
	var res []Pane
	for _, l := range tmux.Lines(out) {
		f := strings.Split(l, tmux.Sep)
		if len(f) < len(paneVars) {
			continue
		}
		g := func(i int) string { return f[i] }
		n := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
		n64 := func(i int) int64 { v, _ := strconv.ParseInt(f[i], 10, 64); return v }
		b := func(i int) bool { return f[i] == "1" || f[i] == "on" || f[i] == "true" }
		order, _ := strconv.ParseFloat(f[18], 64)
		p := Pane{
			ID: g(0), Session: g(1), SessionID: g(2), Window: n(3), Index: n(4),
			PID: n(5), Cmd: g(6), Path: g(7), Dead: b(8), Active: b(9), WinActive: b(10),
			Attached: n(11), Width: n(12), Height: n(13),
			Role: g(14), Kind: g(15), Title: g(16), Group: g(17), Order: order,
			Home: g(19), Dir: g(20), Repo: g(21), Worktree: g(22), Branch: g(23), Base: g(24),
			Inactive: b(25), Read: f[26] != "0", Hooked: b(27), Transcript: g(28),
			HookState: State(g(29)), HookTS: n64(30), BG: n(31), BGKinds: g(32),
			View: State(g(33)), ViewTS: n64(34), Created: n64(35),
			AdoptFlag: b(36) || b(37), WarpGroup: g(38),
		}
		if !p.HookState.Valid() {
			p.HookState = ""
		}
		if !p.View.Valid() {
			p.View = Unknown
		}
		res = append(res, p)
	}
	return res
}

// ListPanes lists every pane on the server.
func ListPanes(t *tmux.Client) ([]Pane, error) {
	out, err := t.Run("list-panes", "-a", "-F", PaneFormat)
	if err != nil {
		if isNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParsePanes(out), nil
}

func isNoServer(err error) bool {
	s := err.Error()
	return strings.Contains(s, "no server running") || strings.Contains(s, "error connecting") || strings.Contains(s, "No such file or directory")
}

// SplitList splits a "\n" separated option value.
func SplitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, "\n") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// ShellCommands are pane_current_command values that mean "just a shell".
var ShellCommands = map[string]bool{
	"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true, "ksh": true,
	"tcsh": true, "csh": true, "nu": true, "elvish": true, "xonsh": true, "-bash": true, "-zsh": true,
	"login": true,
}

// IsShell reports whether cmd is an interactive shell.
func IsShell(cmd string) bool {
	return ShellCommands[strings.TrimPrefix(cmd, "-")] || ShellCommands[cmd]
}
