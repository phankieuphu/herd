// Package hook turns Claude Code and Codex hook calls into pane state.
//
// The hook runs inside the agent's tmux pane, so $TMUX_PANE tells it where
// to write. It must be fast and must never fail the agent: every error is
// swallowed and the process exits 0.
package hook

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/tmux"
)

// ClaudeEvent is the JSON Claude Code sends on stdin to a command hook.
type ClaudeEvent struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	Cwd              string `json:"cwd"`
	HookEventName    string `json:"hook_event_name"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	ToolName         string `json:"tool_name"`
}

// Update is what a hook call wants to write to the pane.
type Update struct {
	State      model.State // "" = leave as is
	BG         *BGCount
	Transcript string
	Hooked     bool
	MarkUnread bool // set when the state becomes something the user should look at
}

// idleNotice matches Claude's "waiting for your input" reminder, which is
// not a request for permission.
func idleNotice(ev ClaudeEvent) bool {
	if ev.NotificationType == "idle_prompt" {
		return true
	}
	m := strings.ToLower(ev.Message)
	return strings.Contains(m, "waiting for your input")
}

// ClaudeUpdate maps one Claude Code hook event to a pane update. prev is the
// state currently stored on the pane; countBG counts background work.
func ClaudeUpdate(ev ClaudeEvent, prev model.State, countBG func(string) BGCount) Update {
	u := Update{Hooked: true, Transcript: ev.TranscriptPath}
	switch ev.HookEventName {
	case "SessionStart":
		u.State = model.Done
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact":
		u.State = model.Working
	case "Notification":
		if idleNotice(ev) {
			if prev == model.Working {
				u.State = model.Done
			}
		} else {
			u.State = model.NeedsYou
		}
	case "Stop", "SubagentStop":
		bg := BGCount{Kinds: map[string]int{}}
		if countBG != nil && ev.TranscriptPath != "" {
			bg = countBG(ev.TranscriptPath)
		}
		u.BG = &bg
		if ev.HookEventName == "SubagentStop" && prev != model.Done && prev != model.Background {
			// a subagent finished while the main turn is still running
			u.State = ""
			break
		}
		if bg.Total > 0 {
			u.State = model.Background
		} else {
			u.State = model.Done
		}
	case "SessionEnd":
		u.State = model.Exited
	}
	if u.State != "" && u.State != prev && (u.State == model.Done || u.State == model.NeedsYou) {
		u.MarkUnread = true
	}
	return u
}

// Commands builds the tmux commands that apply an update to a pane.
func (u Update) Commands(pane string, now time.Time) [][]string {
	var cmds [][]string
	if u.Hooked {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptHooked, "1"))
	}
	if u.Transcript != "" {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptTranscript, u.Transcript))
	}
	if u.State != "" {
		cmds = append(cmds,
			tmux.SetPaneOpt(pane, model.OptState, string(u.State)),
			tmux.SetPaneOpt(pane, model.OptStateTS, strconv.FormatInt(now.UnixMilli(), 10)))
	}
	if u.BG != nil {
		cmds = append(cmds,
			tmux.SetPaneOpt(pane, model.OptBG, strconv.Itoa(u.BG.Total)),
			tmux.SetPaneOpt(pane, model.OptBGKinds, u.BG.KindsString()))
	}
	if u.MarkUnread {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptRead, "0"))
	}
	return cmds
}

func logf(format string, a ...any) {
	path := os.Getenv("HERD_HOOK_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(time.Now().Format(time.RFC3339) + " " + strings.TrimSpace(sprintf(format, a...)) + "\n")
}

func sprintf(format string, a ...any) string {
	b, _ := json.Marshal(a)
	return format + " " + string(b)
}

func currentState(t *tmux.Client, pane string) model.State {
	out, err := t.Run("display-message", "-p", "-t", pane, "#{"+model.OptState+"}")
	if err != nil {
		return ""
	}
	return model.State(strings.TrimSpace(out))
}

// RunClaude handles `herd hook claude`: reads the event from stdin and
// writes the new state to $TMUX_PANE.
func RunClaude(stdin io.Reader) {
	pane := os.Getenv("TMUX_PANE")
	data, _ := io.ReadAll(io.LimitReader(stdin, 4<<20))
	var ev ClaudeEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		logf("claude: bad json", err.Error())
		return
	}
	if pane == "" {
		logf("claude: no TMUX_PANE", ev.HookEventName)
		return
	}
	t := tmux.New()
	prev := currentState(t, pane)
	u := ClaudeUpdate(ev, prev, CountBackgroundFile)
	cmds := u.Commands(pane, time.Now())
	if _, err := t.Batch(cmds...); err != nil {
		logf("claude: tmux", err.Error())
	}
	logf("claude", ev.HookEventName, string(prev), string(u.State))
}

// CodexEvent is the JSON Codex passes to its notify program.
type CodexEvent struct {
	Type                 string `json:"type"`
	LastAssistantMessage string `json:"last-assistant-message"`
}

// CodexUpdate maps a Codex notify event to an update. Codex only reports
// finished turns; "working" comes from reading the screen.
func CodexUpdate(ev CodexEvent) Update {
	if ev.Type == "agent-turn-complete" {
		return Update{State: model.Done, MarkUnread: true}
	}
	return Update{}
}

// RunCodex handles `herd hook codex <json>`.
func RunCodex(args []string) {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" || len(args) == 0 {
		return
	}
	var ev CodexEvent
	if err := json.Unmarshal([]byte(args[len(args)-1]), &ev); err != nil {
		logf("codex: bad json", err.Error())
		return
	}
	u := CodexUpdate(ev)
	if _, err := tmux.New().Batch(u.Commands(pane, time.Now())...); err != nil {
		logf("codex: tmux", err.Error())
	}
}
