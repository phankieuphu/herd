// Package ui is the herd sidebar: a Bubble Tea program that runs in the left
// pane of the main tmux session.
package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/core"
	"github.com/phankieuphu/herd/internal/gitwt"
	"github.com/phankieuphu/herd/internal/limits"
	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/notify"
)

type row struct {
	header bool
	group  string
	count  int
	folded bool
	agent  model.Pane
}

func (r row) height(compact bool) int {
	if r.header || compact {
		return 1
	}
	return 2
}

func (r row) key() string {
	if r.header {
		return "group:" + r.group
	}
	return r.agent.ID
}

// Model is the sidebar state.
type Model struct {
	eng     *core.Engine
	cfg     *config.Store
	tracker *core.Tracker

	snap    *core.Snapshot
	shown   map[string]model.State
	screens map[string]string
	rows    []row
	cursor  int
	offset  int
	selKey  string
	stage   string

	width, height int
	frame         int

	diffs   map[string]gitwt.Stat
	diffAt  time.Time
	limRd   *limits.Reader
	limits  []limits.Provider
	limErr  error
	preview bool

	dlg *dialog

	msg      string
	msgErr   bool
	msgUntil time.Time

	lastRepair time.Time
	notified   map[string]time.Time
	fetching   bool
	cwd        string
	cfgErr     string
}

// New builds the sidebar model.
func New(eng *core.Engine, cfg *config.Store) Model {
	cwd, _ := os.Getwd()
	return Model{
		eng: eng, cfg: cfg, tracker: core.NewTracker(),
		shown: map[string]model.State{}, screens: map[string]string{},
		diffs: map[string]gitwt.Stat{}, notified: map[string]time.Time{},
		preview: cfg.Get().Sidebar.Preview, cwd: cwd, limRd: &limits.Reader{},
	}
}

// ---- messages --------------------------------------------------------------

type tickMsg time.Time
type animMsg time.Time

type snapMsg struct {
	snap    *core.Snapshot
	screens map[string]string
	err     error
}

type diffMsg map[string]gitwt.Stat

type actionMsg struct {
	info    string
	err     error
	select_ string
}

// ConfigMsg tells the sidebar the settings file changed.
type ConfigMsg struct {
	Cfg *config.Config
	Err error
}

// ---- commands --------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), animTick())
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func animTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return animMsg(t) })
}

func (m Model) fetch() tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		s, err := eng.Snapshot()
		if err != nil {
			return snapMsg{err: err}
		}
		var ids []string
		for _, a := range s.Agents() {
			ids = append(ids, a.ID)
		}
		return snapMsg{snap: s, screens: eng.Capture(ids)}
	}
}

func (m Model) action(f func() (string, string, error)) tea.Cmd {
	return func() tea.Msg {
		info, sel, err := f()
		return actionMsg{info: info, err: err, select_: sel}
	}
}

func (m *Model) flash(s string, isErr bool) {
	m.msg, m.msgErr, m.msgUntil = s, isErr, time.Now().Add(5*time.Second)
}

// ---- update ----------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		return m, nil

	case animMsg:
		m.frame++
		return m, animTick()

	case tickMsg:
		if m.fetching {
			return m, tick(m.cfg.Get().Sidebar.Poll.Duration)
		}
		m.fetching = true
		return m, m.fetch()

	case snapMsg:
		m.fetching = false
		next := tick(m.cfg.Get().Sidebar.Poll.Duration)
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
			return m, next
		}
		cmds := []tea.Cmd{next}
		cmds = append(cmds, m.applySnapshot(msg.snap, msg.screens)...)
		return m, tea.Batch(cmds...)

	case diffMsg:
		for k, v := range msg {
			m.diffs[k] = v
		}
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
		} else if msg.info != "" {
			m.flash(msg.info, false)
		}
		if msg.select_ != "" {
			m.selKey = msg.select_
		}
		if m.fetching {
			return m, nil
		}
		m.fetching = true
		return m, m.fetch()

	case ConfigMsg:
		if msg.Err != nil {
			m.cfgErr = msg.Err.Error()
			m.flash("settings: "+msg.Err.Error(), true)
		} else {
			m.cfgErr = ""
			m.cfg.Set(msg.Cfg)
			m.preview = msg.Cfg.Sidebar.Preview
			m.flash("settings reloaded", false)
		}
		return m, nil

	case tea.MouseMsg:
		return m.mouse(msg)

	case tea.KeyMsg:
		if m.dlg != nil {
			return m.dialogKey(msg)
		}
		return m.key(msg)
	}
	return m, nil
}

// applySnapshot runs detection, writes state back to tmux, sends
// notifications and rebuilds the rows.
func (m *Model) applySnapshot(s *core.Snapshot, screens map[string]string) []tea.Cmd {
	cfg := m.cfg.Get()
	now := time.Now()
	var cmds []tea.Cmd

	res := m.tracker.Update(cfg, s, screens, now)
	for i := range s.Panes {
		if st, ok := res.Shown[s.Panes[i].ID]; ok {
			s.Panes[i].View = st
		}
	}
	// reflect read-mark writes right away so the row does not blink
	seen := core.Seen(s)
	for _, t := range res.Transitions {
		if p := s.Find(t.Pane); p != nil && !t.Seen {
			p.Read = false
		}
	}
	for i := range s.Panes {
		if seen[s.Panes[i].ID] {
			s.Panes[i].Read = true
		}
	}
	m.snap, m.shown, m.screens = s, res.Shown, screens
	if st := s.StagedAgent(); st != nil {
		m.stage = st.ID
	} else {
		m.stage = ""
	}

	eng := m.eng
	if len(res.Writes) > 0 {
		w := res.Writes
		cmds = append(cmds, func() tea.Msg { _, _ = eng.T.Batch(w...); return nil })
	}
	if n := m.notifications(cfg, s, res.Transitions, now); n != nil {
		cmds = append(cmds, n)
	}

	// layout repair and adoption, at most every 2 seconds
	if now.Sub(m.lastRepair) > 2*time.Second {
		m.lastRepair = now
		snap := s
		cmds = append(cmds, func() tea.Msg {
			changed, err := eng.Repair(snap)
			n, _ := eng.Adopt(snap)
			if err != nil {
				return actionMsg{err: err}
			}
			if n > 0 {
				return actionMsg{info: fmt.Sprintf("adopted %d session(s)", n)}
			}
			if changed {
				return actionMsg{}
			}
			return nil
		})
	}

	// limit rings
	if lr := cfg.Integrations.LimitRings; lr.Enabled && lr.File != "" {
		m.limRd.Path = config.Expand(lr.File)
		m.limits, m.limErr = m.limRd.Read(now)
	} else {
		m.limits, m.limErr = nil, nil
	}

	// diff stats every 10 seconds
	if now.Sub(m.diffAt) > 10*time.Second {
		m.diffAt = now
		type job struct{ id, dir, base string }
		var jobs []job
		for _, a := range s.Agents() {
			dir := a.Worktree
			if dir == "" {
				dir = a.Repo
			}
			if dir != "" {
				jobs = append(jobs, job{a.ID, dir, a.Base})
			}
		}
		if len(jobs) > 0 {
			cmds = append(cmds, func() tea.Msg {
				out := diffMsg{}
				for _, j := range jobs {
					out[j.id] = gitwt.DiffStat(j.dir, j.base)
				}
				return out
			})
		}
	}

	m.rebuild()
	return cmds
}

func (m *Model) notifications(cfg *config.Config, s *core.Snapshot, ts []core.Transition, now time.Time) tea.Cmd {
	var evs []notify.Event
	for _, t := range ts {
		if t.Seen {
			continue
		}
		p := s.Find(t.Pane)
		if p == nil || p.Inactive {
			continue
		}
		state := string(t.To)
		if !cfg.NotifyOn(state) {
			continue
		}
		key := t.Pane + "/" + state
		if now.Sub(m.notified[key]) < 10*time.Second {
			continue
		}
		m.notified[key] = now
		body := stateWord(t.To)
		if t.To == model.Done && t.From == model.Background {
			body = "done (background work finished)"
		}
		evs = append(evs, notify.Event{Title: t.Title, Body: body, State: state})
	}
	if len(evs) == 0 {
		return nil
	}
	eng, clients := m.eng, s.Clients
	away := notify.Away(clients, now, cfg.Notify.Push.AwayAfter.Duration)
	return func() tea.Msg {
		for _, ev := range evs {
			if cfg.Notify.Tmux {
				notify.Tmux(eng.T, clients, ev)
			}
			if cfg.Notify.Desktop {
				_ = notify.Desktop(ev)
			}
			if cfg.Notify.Push.Enabled && away {
				if err := notify.Push(context.Background(), cfg.Notify.Push, ev); err != nil {
					return actionMsg{err: err}
				}
			}
		}
		return nil
	}
}

// rebuild recomputes rows from the snapshot and keeps the selection.
func (m *Model) rebuild() {
	if m.snap == nil {
		return
	}
	var filter string
	if m.dlg != nil && m.dlg.kind == dlgFilter {
		filter = m.dlg.value(0)
	}
	ordered := m.snap.Ordered()
	var rows []row
	if filter != "" {
		for _, a := range ordered {
			if fuzzy(a.Name()+" "+a.Group+" "+a.Branch+" "+a.Kind, filter) {
				rows = append(rows, row{agent: a})
			}
		}
	} else {
		counts := map[string]int{}
		for _, a := range ordered {
			counts[a.Group]++
		}
		lastGroup := "\x00"
		for _, a := range ordered {
			if a.Group != lastGroup {
				lastGroup = a.Group
				if a.Group != "" {
					rows = append(rows, row{header: true, group: a.Group, count: counts[a.Group], folded: m.snap.Folded[a.Group]})
				}
			}
			if a.Group != "" && m.snap.Folded[a.Group] {
				continue
			}
			rows = append(rows, row{agent: a})
		}
	}
	m.rows = rows
	m.cursor = 0
	found := false
	for i, r := range rows {
		if r.key() == m.selKey {
			m.cursor, found = i, true
			break
		}
	}
	if !found && len(rows) > 0 {
		// prefer the staged agent, then the first agent
		for i, r := range rows {
			if !r.header && r.agent.ID == m.stage {
				m.cursor, found = i, true
				break
			}
		}
		if !found {
			for i, r := range rows {
				if !r.header {
					m.cursor = i
					break
				}
			}
		}
		m.selKey = rows[m.cursor].key()
	}
	m.clampScroll()
}

func (m *Model) selected() *row {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

func (m *Model) selectedAgent() *model.Pane {
	r := m.selected()
	if r == nil || r.header {
		return nil
	}
	return &r.agent
}

func (m *Model) move(d int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += d
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	m.selKey = m.rows[m.cursor].key()
	m.clampScroll()
}

// clampScroll keeps the cursor inside the visible list area.
func (m *Model) clampScroll() {
	h := m.listHeight()
	if h <= 0 || len(m.rows) == 0 {
		m.offset = 0
		return
	}
	compact := m.cfg.Get().Sidebar.Compact
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	for {
		used := 0
		for i := m.offset; i <= m.cursor && i < len(m.rows); i++ {
			used += m.rows[i].height(compact)
		}
		if used <= h || m.offset >= m.cursor {
			break
		}
		m.offset++
	}
}

func (m Model) showCmd(id string, focus bool) tea.Cmd {
	eng := m.eng
	return m.action(func() (string, string, error) {
		return "", id, eng.Show(id, focus)
	})
}

// ---- keys ------------------------------------------------------------------

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	cfg := m.cfg.Get()
	sel := m.selectedAgent()
	r := m.selected()
	switch k.String() {
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "home":
		m.cursor = 0
		m.move(0)
	case "end", "G":
		m.cursor = len(m.rows) - 1
		m.move(0)
	case "enter":
		if r != nil && r.header {
			return m, m.foldCmd(r.group)
		}
		if sel != nil {
			return m, m.showCmd(sel.ID, true)
		}
	case " ", "l", "right":
		if r != nil && r.header {
			return m, m.foldCmd(r.group)
		}
		if sel != nil {
			return m, m.showCmd(sel.ID, false)
		}
	case "tab", "shift+tab":
		if m.snap == nil {
			return m, nil
		}
		dir := 1
		if k.String() == "shift+tab" {
			dir = -1
		}
		cur := m.stage
		if sel != nil {
			cur = sel.ID
		}
		id := core.NextAttention(m.snap.Ordered(), cur, dir)
		if id == "" {
			m.flash("no agent needs you", false)
			return m, nil
		}
		m.selKey = id
		m.rebuild()
		return m, m.showCmd(id, false)
	case "n":
		m.openNew("")
		return m, m.dlg.focusInput()
	case "N":
		names := cfg.PresetNames()
		if len(names) == 0 {
			m.flash("no presets in settings.toml", true)
			return m, nil
		}
		m.dlg = &dialog{kind: dlgPreset, title: "Start from preset", options: names}
	case "r":
		if r != nil && r.header {
			m.dlg = &dialog{kind: dlgRenameGroup, title: "Rename group", target: r.group,
				inputs: []textinput.Model{newInput("group name", r.group, m.width-4)}}
			return m, m.dlg.focusInput()
		}
		if sel != nil {
			m.dlg = &dialog{kind: dlgRename, title: "Rename", target: sel.ID,
				inputs: []textinput.Model{newInput("name", sel.Name(), m.width-4)}}
			return m, m.dlg.focusInput()
		}
	case "m":
		if sel != nil {
			m.dlg = &dialog{kind: dlgMessage, title: "Message to " + sel.Name(), target: sel.ID,
				inputs: []textinput.Model{newInput("type and press enter", "", m.width-4)}}
			return m, m.dlg.focusInput()
		}
	case "a", "A":
		if sel == nil {
			return m, nil
		}
		if sel.View != model.NeedsYou {
			m.flash("nothing to answer: "+sel.Name()+" is "+stateWord(sel.View), true)
			return m, nil
		}
		ac := cfg.Agents[sel.Kind]
		keys, word := ac.ApproveKeys, "allowed"
		if k.String() == "A" {
			keys, word = ac.DenyKeys, "denied"
		}
		id, eng := sel.ID, m.eng
		return m, m.action(func() (string, string, error) {
			return word, "", eng.SendKeys(id, keys)
		})
	case "g":
		if sel != nil {
			groups := []string{}
			if m.snap != nil {
				groups = m.snap.GroupOrder()
			}
			m.dlg = &dialog{kind: dlgGroup, title: "Move to group (empty = none, tab = complete)", target: sel.ID,
				options: groups, inputs: []textinput.Model{newInput("group", sel.Group, m.width-4)}}
			return m, m.dlg.focusInput()
		}
	case "J", "K", "shift+down", "shift+up":
		d := 1
		if k.String() == "K" || k.String() == "shift+up" {
			d = -1
		}
		snap, eng := m.snap, m.eng
		if snap == nil {
			return m, nil
		}
		if r != nil && r.header {
			g := r.group
			return m, m.action(func() (string, string, error) { return "", "", eng.MoveGroup(snap, g, d) })
		}
		if sel != nil {
			id := sel.ID
			return m, m.action(func() (string, string, error) { return "", "", eng.Reorder(snap, id, d) })
		}
	case "z":
		if sel != nil {
			id, on, eng := sel.ID, !sel.Inactive, m.eng
			return m, m.action(func() (string, string, error) {
				word := "set aside"
				if !on {
					word = "back in play"
				}
				return word, "", eng.SetFlag(id, model.OptInactive, on)
			})
		}
	case "u":
		if sel != nil {
			id, read, eng := sel.ID, !sel.Read, m.eng
			return m, m.action(func() (string, string, error) { return "", "", eng.SetFlag(id, model.OptRead, read) })
		}
	case "d":
		if sel != nil {
			dir := sel.Worktree
			if dir == "" {
				dir = sel.Repo
			}
			if dir == "" {
				m.flash("not a git folder", true)
				return m, nil
			}
			base, eng := sel.Base, m.eng
			return m, m.action(func() (string, string, error) { return "", "", eng.Popup(dir, gitwt.DiffScript(base)) })
		}
	case "D":
		if sel != nil {
			if sel.Worktree == "" {
				m.flash("this agent has no worktree", true)
				return m, nil
			}
			a, eng := *sel, m.eng
			force := false
			text := "Close " + a.Name() + " and remove worktree " + a.Branch + "?"
			if gitwt.Dirty(a.Worktree) {
				force = true
				text = "Worktree has UNCOMMITTED changes. Remove anyway and lose them?"
			}
			m.dlg = &dialog{kind: dlgConfirm, title: text, onYes: func() tea.Cmd {
				return m.action(func() (string, string, error) {
					res, err := eng.RemoveWorktree(a, force)
					if err != nil {
						return "", "", err
					}
					info := "worktree removed"
					if res.BranchNote != "" {
						info += "; " + res.BranchNote
					} else if res.BranchDeleted {
						info += " and branch deleted"
					}
					return info, "", nil
				})
			}}
		}
	case "x":
		if sel != nil {
			id, name, eng := sel.ID, sel.Name(), m.eng
			text := "Close " + name + "? Its tmux session ends."
			if sel.Worktree != "" {
				text += " The worktree stays (D removes it)."
			}
			m.dlg = &dialog{kind: dlgConfirm, title: text, onYes: func() tea.Cmd {
				return m.action(func() (string, string, error) { return "closed " + name, "", eng.Close(id) })
			}}
		}
	case "p":
		m.preview = !m.preview
	case "/":
		m.dlg = &dialog{kind: dlgFilter, title: "Filter", inputs: []textinput.Model{newInput("type to filter", "", m.width-4)}}
		return m, m.dlg.focusInput()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
	case "q":
		eng := m.eng
		return m, m.action(func() (string, string, error) {
			_, err := eng.T.Run("detach-client")
			return "", "", err
		})
	case "Q":
		eng := m.eng
		m.dlg = &dialog{kind: dlgConfirm, title: "Close the herd window? Agents keep running; run `herd` to come back.", onYes: func() tea.Cmd {
			return m.action(func() (string, string, error) { return "", "", eng.Quit() })
		}}
	case "esc":
		m.msg = ""
	case "ctrl+c":
		// the sidebar should not die by accident; use Q
		m.flash("press Q to close herd, q to detach", false)
	}
	return m, nil
}

func (m Model) foldCmd(group string) tea.Cmd {
	snap, eng := m.snap, m.eng
	if snap == nil {
		return nil
	}
	return m.action(func() (string, string, error) { return "", "group:" + group, eng.ToggleFold(snap, group) })
}

func (m *Model) openNew(preset string) {
	cfg := m.cfg.Get()
	dir := m.cwd
	group := ""
	if sel := m.selectedAgent(); sel != nil {
		if sel.Dir != "" {
			dir = sel.Dir
		}
		group = sel.Group
	} else if r := m.selected(); r != nil && r.header {
		group = r.group
	}
	kinds := cfg.AgentNames()
	d := &dialog{kind: dlgNew, title: "New agent", kinds: kinds, worktree: cfg.Defaults.Worktree, focus: 1, target: group}
	for i, k := range kinds {
		if k == cfg.Defaults.Agent {
			d.kindIdx = i
		}
	}
	w := m.width - 4
	d.inputs = []textinput.Model{
		newInput("folder", shortHome(dir), w),
		newInput("name (optional)", "", w),
		newInput("first prompt (optional)", "", w),
	}
	d.labels = []string{"agent", "folder", "name", "prompt", "worktree"}
	d.folderSuggest()
	m.dlg = d
}

func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func (m Model) dialogKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.dlg
	key := k.String()
	switch d.kind {
	case dlgHelp:
		m.dlg = nil
		return m, nil
	case dlgConfirm:
		switch key {
		case "y", "Y", "enter":
			m.dlg = nil
			return m, d.onYes()
		case "n", "N", "esc", "q":
			m.dlg = nil
		}
		return m, nil
	case dlgPreset:
		switch key {
		case "up", "k":
			if d.optIdx > 0 {
				d.optIdx--
			}
		case "down", "j":
			if d.optIdx < len(d.options)-1 {
				d.optIdx++
			}
		case "esc", "q":
			m.dlg = nil
		case "enter":
			m.dlg = nil
			cfg := m.cfg.Get()
			o, err := core.FromPreset(cfg, d.options[d.optIdx], core.NewOpts{})
			if err != nil {
				m.flash(err.Error(), true)
				return m, nil
			}
			if o.Dir == "" {
				o.Dir = m.cwd
			}
			return m, m.createCmd(o)
		}
		return m, nil
	}

	switch key {
	case "esc":
		if d.kind == dlgFilter {
			m.dlg = nil
			m.rebuild()
			return m, nil
		}
		m.dlg = nil
		return m, nil
	case "tab", "down":
		if d.kind == dlgGroup && key == "tab" {
			d.complete()
			return m, nil
		}
		if d.kind == dlgNew && d.focus == 1 && key == "tab" && d.folderComplete(1) {
			return m, nil
		}
		if d.kind == dlgFilter {
			if key == "down" {
				m.move(1)
			}
			return m, nil
		}
		if d.fields() > 1 {
			d.focus = (d.focus + 1) % d.fields()
			if d.kind == dlgNew {
				d.folderSuggest()
			}
			return m, d.focusInput()
		}
		return m, nil
	case "shift+tab", "up":
		if d.kind == dlgFilter {
			if key == "up" {
				m.move(-1)
			}
			return m, nil
		}
		if d.kind == dlgNew && d.focus == 1 && key == "shift+tab" && d.cycling {
			d.folderComplete(-1)
			return m, nil
		}
		if d.fields() > 1 {
			d.focus = (d.focus - 1 + d.fields()) % d.fields()
			if d.kind == dlgNew {
				d.folderSuggest()
			}
			return m, d.focusInput()
		}
		return m, nil
	case "enter":
		return m.submit()
	}

	if d.kind == dlgNew && d.inputIndex() < 0 {
		switch {
		case d.focus == 0 && (key == "left" || key == "h"):
			d.kindIdx = (d.kindIdx - 1 + len(d.kinds)) % len(d.kinds)
		case d.focus == 0 && (key == "right" || key == "l" || key == " "):
			d.kindIdx = (d.kindIdx + 1) % len(d.kinds)
		case d.focus == 4 && (key == " " || key == "left" || key == "right" || key == "h" || key == "l"):
			d.worktree = !d.worktree
		}
		return m, nil
	}
	i := d.inputIndex()
	if i < 0 {
		return m, nil
	}
	var cmd tea.Cmd
	d.inputs[i], cmd = d.inputs[i].Update(k)
	if d.kind == dlgFilter {
		m.rebuild()
	}
	if d.kind == dlgNew && d.focus == 1 {
		d.folderSuggest()
	}
	return m, cmd
}

func (m Model) createCmd(o core.NewOpts) tea.Cmd {
	eng := m.eng
	return m.action(func() (string, string, error) {
		res, err := eng.NewAgent(o)
		if err != nil {
			return "", "", err
		}
		if err := eng.Show(res.Pane, true); err != nil {
			return "", res.Pane, err
		}
		info := "started " + strings.TrimPrefix(res.Session, "herd-")
		if res.Note != "" {
			info += " (" + res.Note + ")"
		}
		return info, res.Pane, nil
	})
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	d := m.dlg
	eng := m.eng
	m.dlg = nil
	switch d.kind {
	case dlgNew:
		o := core.NewOpts{
			Kind: d.kinds[d.kindIdx], Dir: d.value(0), Name: d.value(1), Prompt: d.value(2),
			Group: d.target, Worktree: d.worktree,
		}
		return m, m.createCmd(o)
	case dlgRename:
		a := m.snap.Find(d.target)
		if a == nil {
			return m, nil
		}
		pane, title := *a, d.value(0)
		return m, m.action(func() (string, string, error) { return "", "", eng.SetTitle(pane, title) })
	case dlgRenameGroup:
		old, name := d.target, d.value(0)
		snap := m.snap
		return m, m.action(func() (string, string, error) {
			for _, a := range snap.Agents() {
				if a.Group == old {
					if err := eng.SetGroup(a, name); err != nil {
						return "", "", err
					}
				}
			}
			return "", "group:" + name, nil
		})
	case dlgGroup:
		a := m.snap.Find(d.target)
		if a == nil {
			return m, nil
		}
		pane, g := *a, d.value(0)
		return m, m.action(func() (string, string, error) { return "", pane.ID, eng.SetGroup(pane, g) })
	case dlgMessage:
		id, text := d.target, d.inputs[0].Value()
		if strings.TrimSpace(text) == "" {
			return m, nil
		}
		return m, m.action(func() (string, string, error) { return "sent", "", eng.SendText(id, text) })
	case dlgFilter:
		// the filter is closed now; rebuild keeps the selected agent
		m.rebuild()
		if sel := m.selectedAgent(); sel != nil {
			return m, m.showCmd(sel.ID, true)
		}
	}
	return m, nil
}

// ---- mouse -----------------------------------------------------------------

func (m Model) mouse(ev tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.dlg != nil {
		return m, nil
	}
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		m.move(-1)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.move(1)
		return m, nil
	}
	if ev.Action != tea.MouseActionPress || ev.Button != tea.MouseButtonLeft {
		return m, nil
	}
	idx := m.rowAt(ev.Y)
	if idx < 0 {
		return m, nil
	}
	m.cursor = idx
	m.selKey = m.rows[idx].key()
	r := m.rows[idx]
	if r.header {
		return m, m.foldCmd(r.group)
	}
	return m, m.showCmd(r.agent.ID, true)
}

// rowAt maps a screen line to a row index (-1 when none).
func (m Model) rowAt(y int) int {
	compact := m.cfg.Get().Sidebar.Compact
	line := topLines
	h := m.listHeight()
	for i := m.offset; i < len(m.rows); i++ {
		rh := m.rows[i].height(compact)
		if line-topLines+rh > h {
			break
		}
		if y >= line && y < line+rh {
			return i
		}
		line += rh
	}
	return -1
}
