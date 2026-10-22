// Package detect decides what an agent is doing from hook reports and from
// the text on its screen.
package detect

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/model"
)

// Rules are compiled screen patterns for one agent kind.
type Rules struct {
	NeedsYou []*regexp.Regexp
	Working  []*regexp.Regexp
}

var (
	cacheMu sync.Mutex
	cache   = map[string]*regexp.Regexp{}
)

func compile(pats []string) []*regexp.Regexp {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	var out []*regexp.Regexp
	for _, p := range pats {
		re, ok := cache[p]
		if !ok {
			var err error
			re, err = regexp.Compile("(?m)" + p)
			if err != nil {
				re = nil
			}
			cache[p] = re
		}
		if re != nil {
			out = append(out, re)
		}
	}
	return out
}

// Compile builds Rules from config. Bad patterns are skipped.
func Compile(a config.Agent) Rules {
	return Rules{NeedsYou: compile(a.NeedsYou), Working: compile(a.Working)}
}

// BadPatterns returns patterns that do not compile, for `herd doctor`.
func BadPatterns(a config.Agent) []string {
	var bad []string
	for _, p := range append(append([]string{}, a.NeedsYou...), a.Working...) {
		if _, err := regexp.Compile(p); err != nil {
			bad = append(bad, p)
		}
	}
	return bad
}

// tail keeps the last n non-empty lines; agents draw their status near the bottom.
func tail(screen string, n int) string {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			keep = append(keep, lines[i])
		}
	}
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	return strings.Join(keep, "\n")
}

// Scrape reads a screen. NeedsYou wins over Working; anything else is Done.
// An empty screen is Unknown.
func Scrape(r Rules, screen string) model.State {
	t := tail(screen, 25)
	if strings.TrimSpace(t) == "" {
		return model.Unknown
	}
	for _, re := range r.NeedsYou {
		if re.MatchString(t) {
			return model.NeedsYou
		}
	}
	for _, re := range r.Working {
		if re.MatchString(t) {
			return model.Working
		}
	}
	return model.Done
}

// Input is everything Resolve needs about one pane.
type Input struct {
	Kind      string
	Cmd       string // pane_current_command
	Created   time.Time
	Hooked    bool
	HookState model.State
	BG        int
	Scraped   model.State
}

// StartGrace is how long a new pane may sit at a shell prompt before we call
// the agent exited (the command is still being typed into the shell).
const StartGrace = 5 * time.Second

// Resolve combines hook state and screen state into one state.
func Resolve(in Input, now time.Time) model.State {
	if in.Kind == "shell" {
		if model.IsShell(in.Cmd) {
			return model.Done
		}
		return model.Working
	}
	if model.IsShell(in.Cmd) {
		// a wrapper script waiting at a prompt still needs the user
		if in.Scraped == model.NeedsYou {
			return model.NeedsYou
		}
		if now.Sub(in.Created) < StartGrace {
			return model.Working
		}
		return model.Exited
	}
	if in.Hooked && in.HookState != "" && in.HookState != model.Unknown {
		h := in.HookState
		if h == model.Exited {
			return model.Exited
		}
		// The screen sees approval prompts and answers before hooks do.
		if in.Scraped == model.NeedsYou {
			return model.NeedsYou
		}
		if h == model.NeedsYou && in.Scraped == model.Working {
			return model.Working
		}
		if h == model.Done && in.BG > 0 {
			return model.Background
		}
		if h == model.Background && in.BG == 0 {
			return model.Done
		}
		return h
	}
	if in.Scraped == "" {
		return model.Unknown
	}
	return in.Scraped
}

// Debouncer smooths Working -> Done flicker: spinners and status lines blink,
// so an agent must look idle for Grace before we call it done.
type Debouncer struct {
	Grace   time.Duration
	shown   model.State
	pending model.State
	since   time.Time
}

// Feed returns the state to show for a new raw state.
func (d *Debouncer) Feed(raw model.State, now time.Time) model.State {
	if d.shown == "" {
		d.shown = raw
		return raw
	}
	if raw == d.shown {
		d.pending = ""
		return d.shown
	}
	leavingWork := d.shown.Busy() && (raw == model.Done || raw == model.Unknown)
	if !leavingWork || d.Grace <= 0 {
		d.shown, d.pending = raw, ""
		return raw
	}
	if d.pending != raw {
		d.pending, d.since = raw, now
		return d.shown
	}
	if now.Sub(d.since) >= d.Grace {
		d.shown, d.pending = raw, ""
	}
	return d.shown
}

// Shown is the current shown state.
func (d *Debouncer) Shown() model.State { return d.shown }
