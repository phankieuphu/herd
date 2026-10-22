package core

import (
	"os/exec"
	"strconv"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/detect"
	"github.com/phankieuphu/herd/internal/hook"
	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/tmux"
)

var execCommand = exec.Command

// Transition is a change of shown state worth telling the user about.
type Transition struct {
	Pane  string
	Title string
	From  model.State
	To    model.State
	Seen  bool // the user is looking at this pane
}

// Tracker turns raw observations into shown states, unread marks and
// notifications. The sidebar owns one tracker.
type Tracker struct {
	Grace      time.Duration
	deb        map[string]*detect.Debouncer
	bgChecked  map[string]time.Time
	CountBG    func(path string) hook.BGCount
	BGInterval time.Duration
}

// NewTracker returns a tracker with sensible defaults.
func NewTracker() *Tracker {
	return &Tracker{
		Grace:      2 * time.Second,
		deb:        map[string]*detect.Debouncer{},
		bgChecked:  map[string]time.Time{},
		CountBG:    hook.CountBackgroundFile,
		BGInterval: 5 * time.Second,
	}
}

// Seen returns panes the user is looking at: each client's active pane, and
// the stage pane when a client is attached to the main session.
func Seen(s *Snapshot) map[string]bool {
	seen := map[string]bool{}
	st := s.Stage()
	for _, c := range s.Clients {
		seen[c.Pane] = true
		if c.Session == s.Main && st != nil {
			seen[st.ID] = true
		}
	}
	return seen
}

// Result is what one Update produced.
type Result struct {
	Writes      [][]string
	Transitions []Transition
	// Shown maps pane id to the state to display.
	Shown map[string]model.State
}

// Update processes one snapshot. screens maps pane id to visible text.
func (tr *Tracker) Update(cfg *config.Config, s *Snapshot, screens map[string]string, now time.Time) Result {
	res := Result{Shown: map[string]model.State{}}
	seen := Seen(s)
	alive := map[string]bool{}
	for i := range s.Panes {
		p := &s.Panes[i]
		if !p.IsAgent() {
			continue
		}
		alive[p.ID] = true
		ac, ok := cfg.Agents[p.Kind]
		if !ok {
			ac = config.Agent{}
		}

		// keep background counts fresh while work runs after the turn
		bg := p.BG
		if p.Hooked && p.HookState == model.Background && p.Transcript != "" && tr.CountBG != nil &&
			now.Sub(tr.bgChecked[p.ID]) >= tr.BGInterval {
			tr.bgChecked[p.ID] = now
			c := tr.CountBG(p.Transcript)
			if c.Total != p.BG {
				bg = c.Total
				res.Writes = append(res.Writes,
					tmux.SetPaneOpt(p.ID, model.OptBG, strconv.Itoa(c.Total)),
					tmux.SetPaneOpt(p.ID, model.OptBGKinds, c.KindsString()))
				if c.Total == 0 {
					res.Writes = append(res.Writes, tmux.SetPaneOpt(p.ID, model.OptState, string(model.Done)))
				}
			}
		}

		scraped := model.State("")
		if scr, ok := screens[p.ID]; ok {
			scraped = detect.Scrape(detect.Compile(ac), scr)
		}
		raw := detect.Resolve(detect.Input{
			Kind: p.Kind, Cmd: p.Cmd, Created: time.Unix(p.Created, 0),
			Hooked: p.Hooked, HookState: p.HookState, BG: bg, Scraped: scraped,
		}, now)

		d := tr.deb[p.ID]
		first := d == nil
		if first {
			d = &detect.Debouncer{Grace: tr.Grace}
			tr.deb[p.ID] = d
			if p.View != model.Unknown && p.View != "" {
				d.Feed(p.View, now) // continue from what was shown before a restart
			}
		}
		prev := d.Shown()
		shown := d.Feed(raw, now)
		if shown == model.Unknown && prev != "" && prev != model.Unknown {
			shown = prev
		}
		res.Shown[p.ID] = shown

		if shown != p.View {
			res.Writes = append(res.Writes,
				tmux.SetPaneOpt(p.ID, model.OptView, string(shown)),
				tmux.SetPaneOpt(p.ID, model.OptViewTS, strconv.FormatInt(now.Unix(), 10)))
		}
		isSeen := seen[p.ID]
		if isSeen && !p.Read {
			res.Writes = append(res.Writes, tmux.SetPaneOpt(p.ID, model.OptRead, "1"))
		}
		changed := prev != "" && prev != model.Unknown && shown != prev
		if changed && shown.Attention() {
			if !isSeen && p.Read {
				res.Writes = append(res.Writes, tmux.SetPaneOpt(p.ID, model.OptRead, "0"))
			}
			res.Transitions = append(res.Transitions, Transition{
				Pane: p.ID, Title: p.Name(), From: prev, To: shown, Seen: isSeen,
			})
		}
	}
	for id := range tr.deb {
		if !alive[id] {
			delete(tr.deb, id)
			delete(tr.bgChecked, id)
		}
	}
	return res
}
