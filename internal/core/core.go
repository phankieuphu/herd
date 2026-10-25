// Package core holds the tmux operations behind herd: the layout of the main
// session, showing an agent in the stage pane, and creating, moving and
// closing agents.
//
// Layout: the main session (default "herd") has one window with two panes,
// the sidebar on the left and the "stage" on the right. Every agent lives in
// its own tmux session ("herd-<slug>"). To show an agent, herd swaps its pane
// into the stage; a placeholder pane takes its place in the agent's own
// session, so the session stays alive. Invariant: the placeholder is either
// on the stage or in the home session of the agent on the stage.
package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/gitwt"
	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/tmux"
)

// Engine runs herd operations against one tmux server.
type Engine struct {
	T    *tmux.Client
	Cfg  *config.Store
	Self string // absolute path of the herd binary
}

// New returns an engine for the default tmux server.
func New(cfg *config.Store) *Engine {
	self, err := os.Executable()
	if err != nil {
		self = "herd"
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return &Engine{T: tmux.New(), Cfg: cfg, Self: self}
}

// Main is the main session name.
func (e *Engine) Main() string { return e.Cfg.Get().Sidebar.Session }

// Snapshot is the state of the tmux server at one moment.
type Snapshot struct {
	Main    string
	Panes   []model.Pane
	Clients []tmux.ClientInfo
	Groups  []string // saved group order
	Folded  map[string]bool
	At      time.Time
}

// Snapshot reads all panes, clients and global herd options.
func (e *Engine) Snapshot() (*Snapshot, error) {
	panes, err := model.ListPanes(e.T)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{Main: e.Main(), Panes: panes, At: time.Now(), Folded: map[string]bool{}}
	if len(panes) == 0 {
		return s, nil
	}
	s.Clients, _ = e.T.ListClients()
	out, _ := e.T.Run("show-options", "-gqv", model.OptGroups)
	s.Groups = model.SplitList(out)
	out, _ = e.T.Run("show-options", "-gqv", model.OptCollapsed)
	for _, g := range model.SplitList(out) {
		s.Folded[g] = true
	}
	return s, nil
}

// Find returns the pane with this id.
func (s *Snapshot) Find(id string) *model.Pane {
	for i := range s.Panes {
		if s.Panes[i].ID == id {
			return &s.Panes[i]
		}
	}
	return nil
}

// Sidebar returns the sidebar pane of the main session.
func (s *Snapshot) Sidebar() *model.Pane {
	for i := range s.Panes {
		p := &s.Panes[i]
		if p.Session == s.Main && p.Role == model.RoleSidebar && !p.Dead {
			return p
		}
	}
	return nil
}

// Stage returns the pane next to the sidebar (placeholder or an agent).
func (s *Snapshot) Stage() *model.Pane {
	sb := s.Sidebar()
	if sb == nil {
		return nil
	}
	for i := range s.Panes {
		p := &s.Panes[i]
		if p.Session == s.Main && p.Window == sb.Window && p.ID != sb.ID && !p.Dead {
			return p
		}
	}
	return nil
}

// StagedAgent returns the agent on the stage, or nil.
func (s *Snapshot) StagedAgent() *model.Pane {
	st := s.Stage()
	if st != nil && st.IsAgent() {
		return st
	}
	return nil
}

// Agents returns every agent pane.
func (s *Snapshot) Agents() []model.Pane {
	var out []model.Pane
	for _, p := range s.Panes {
		if p.IsAgent() {
			out = append(out, p)
		}
	}
	return out
}

// HasSession reports whether a session exists in the snapshot.
func (s *Snapshot) HasSession(name string) bool {
	for _, p := range s.Panes {
		if p.Session == name {
			return true
		}
	}
	return false
}

func (s *Snapshot) placeholderIn(session string) *model.Pane {
	for i := range s.Panes {
		p := &s.Panes[i]
		if p.Session == session && p.Role == model.RolePlaceholder && !p.Dead {
			return p
		}
	}
	return nil
}

// GroupOrder returns groups in display order: saved order first, then any
// other groups in use, alphabetically.
func (s *Snapshot) GroupOrder() []string {
	seen := map[string]bool{}
	var out []string
	inUse := map[string]bool{}
	for _, a := range s.Agents() {
		if a.Group != "" {
			inUse[a.Group] = true
		}
	}
	for _, g := range s.Groups {
		if !seen[g] && inUse[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	var rest []string
	for g := range inUse {
		if !seen[g] {
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Ordered returns agents in display order: ungrouped first, then by group
// order; inside a group by @herd_order, then creation time.
func (s *Snapshot) Ordered() []model.Pane {
	agents := s.Agents()
	rank := map[string]int{"": -1}
	for i, g := range s.GroupOrder() {
		rank[g] = i
	}
	sort.SliceStable(agents, func(i, j int) bool {
		a, b := agents[i], agents[j]
		if rank[a.Group] != rank[b.Group] {
			return rank[a.Group] < rank[b.Group]
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if a.Created != b.Created {
			return a.Created < b.Created
		}
		return a.ID < b.ID
	})
	return agents
}

// ---- layout ---------------------------------------------------------------

func (e *Engine) sidebarCmd() string {
	return "while :; do " + tmux.Quote(e.Self) + " sidebar && break; sleep 1; done"
}

// RestartSidebar restarts the sidebar process, so a new herd binary takes
// over. Everything the sidebar shows lives in tmux, so nothing is lost.
func (e *Engine) RestartSidebar() error {
	s, err := e.Snapshot()
	if err != nil {
		return err
	}
	sb := s.Sidebar()
	if sb == nil {
		return nil
	}
	_, err = e.T.Run("respawn-pane", "-k", "-t", sb.ID, e.sidebarCmd())
	return err
}

func (e *Engine) placeholderCmd() string { return tmux.Quote(e.Self) + " placeholder" }

// Create makes the main session if it does not exist. cols/rows size the
// detached session before the first client attaches.
func (e *Engine) Create(cols, rows int, dir string) error {
	main := e.Main()
	if e.T.HasSession(main) {
		return nil
	}
	if cols < 60 {
		cols = 200
	}
	if rows < 10 {
		rows = 50
	}
	stage, err := e.T.Run("new-session", "-d", "-s", main, "-n", "herd", "-c", dir,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows), "-P", "-F", "#{pane_id}", e.placeholderCmd())
	if err != nil {
		return err
	}
	w := e.Cfg.Get().Sidebar.Width
	sb, err := e.T.Run("split-window", "-h", "-b", "-l", strconv.Itoa(w), "-t", stage, "-c", dir,
		"-P", "-F", "#{pane_id}", e.sidebarCmd())
	if err != nil {
		return err
	}
	_, err = e.T.Batch(
		tmux.SetPaneOpt(stage, model.OptRole, model.RolePlaceholder),
		tmux.SetPaneOpt(sb, model.OptRole, model.RoleSidebar),
		[]string{"select-pane", "-t", sb},
	)
	if err != nil {
		return err
	}
	e.BindKeys()
	return nil
}

// BindKeys installs the focus-sidebar and jump keys from the settings
// (prefix C-h and prefix C-j by default). Keys bound by an earlier run are
// removed first, so a changed setting does not leave the old key behind.
func (e *Engine) BindKeys() {
	t := e.Cfg.Get().Integrations.Tmux
	var cmds [][]string
	old, _ := e.T.Run("show-options", "-gqv", model.OptKeys)
	for _, l := range tmux.Lines(old) {
		if f := strings.Fields(l); len(f) == 2 {
			cmds = append(cmds, []string{"unbind-key", "-q", "-T", f[0], f[1]})
		}
	}
	var bound []string
	if t.BindKeys {
		self := tmux.Quote(e.Self)
		for _, b := range []struct{ spec, cmd string }{{t.FocusKey, "focus-sidebar"}, {t.JumpKey, "jump"}} {
			table, key, ok := config.KeySpec(b.spec)
			if !ok {
				continue
			}
			cmds = append(cmds, []string{"bind-key", "-T", table, key, "run-shell", "-b", self + " " + b.cmd})
			bound = append(bound, table+" "+key)
		}
	}
	cmds = append(cmds, []string{"set-option", "-g", model.OptKeys, strings.Join(bound, "\n")})
	_, _ = e.T.Batch(cmds...)
}

// Repair fixes a broken layout: a missing sidebar or stage pane, sessions
// that only hold a placeholder, and a sidebar with the wrong width. It
// returns true when it changed something.
func (e *Engine) Repair(s *Snapshot) (bool, error) {
	if !s.HasSession(s.Main) {
		return false, nil
	}
	changed := false
	sb := s.Sidebar()
	st := s.Stage()
	w := e.Cfg.Get().Sidebar.Width
	switch {
	case sb == nil:
		// put a sidebar to the left of the first pane in the main session
		var target string
		for _, p := range s.Panes {
			if p.Session == s.Main && !p.Dead {
				target = p.ID
				break
			}
		}
		if target == "" {
			return false, nil
		}
		id, err := e.T.Run("split-window", "-h", "-b", "-l", strconv.Itoa(w), "-t", target, "-P", "-F", "#{pane_id}", e.sidebarCmd())
		if err != nil {
			return false, err
		}
		_, _ = e.T.Batch(tmux.SetPaneOpt(id, model.OptRole, model.RoleSidebar))
		changed = true
	case st == nil:
		id, err := e.T.Run("split-window", "-h", "-d", "-t", sb.ID, "-P", "-F", "#{pane_id}", e.placeholderCmd())
		if err != nil {
			return false, err
		}
		_, _ = e.T.Batch(tmux.SetPaneOpt(id, model.OptRole, model.RolePlaceholder),
			[]string{"resize-pane", "-t", sb.ID, "-x", strconv.Itoa(w)})
		changed = true
	case sb.Width != w && st != nil:
		_, _ = e.T.Run("resize-pane", "-t", sb.ID, "-x", strconv.Itoa(w))
	}
	// Sessions that only hold placeholders and are nobody's home are leftovers
	// of agents that exited while shown on the stage.
	homes := map[string]bool{}
	for _, a := range s.Agents() {
		homes[a.Home] = true
	}
	bySession := map[string][]model.Pane{}
	for _, p := range s.Panes {
		bySession[p.Session] = append(bySession[p.Session], p)
	}
	for sess, panes := range bySession {
		if sess == s.Main || homes[sess] {
			continue
		}
		all := true
		for _, p := range panes {
			if p.Role != model.RolePlaceholder {
				all = false
				break
			}
		}
		if all {
			_, _ = e.T.Run("kill-session", "-t", "="+sess)
			changed = true
		}
	}
	return changed, nil
}

// ---- stage ----------------------------------------------------------------

// homeSlot returns the pane id of a placeholder in the agent's home session,
// creating the session or the placeholder when missing.
func (e *Engine) homeSlot(s *Snapshot, a *model.Pane) (string, error) {
	home := a.Home
	if home == "" || home == s.Main {
		home = "herd-" + gitwt.Slug(a.Name())
	}
	if ph := s.placeholderIn(home); ph != nil {
		return ph.ID, nil
	}
	dir := a.Path
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	var id string
	var err error
	if s.HasSession(home) {
		id, err = e.T.Run("new-window", "-d", "-t", "="+home+":", "-c", dir, "-P", "-F", "#{pane_id}", e.placeholderCmd())
	} else {
		args := []string{"new-session", "-d", "-s", home, "-c", dir, "-P", "-F", "#{pane_id}"}
		if a.Width > 0 && a.Height > 0 {
			args = append(args, "-x", strconv.Itoa(a.Width), "-y", strconv.Itoa(a.Height))
		}
		id, err = e.T.Run(append(args, e.placeholderCmd())...)
	}
	if err != nil {
		return "", err
	}
	cmds := [][]string{tmux.SetPaneOpt(id, model.OptRole, model.RolePlaceholder)}
	if home != a.Home {
		cmds = append(cmds, tmux.SetPaneOpt(a.ID, model.OptHome, home))
	}
	_, err = e.T.Batch(cmds...)
	return id, err
}

// Show puts an agent on the stage. With focus, the agent pane gets the
// keyboard; otherwise the sidebar keeps it.
func (e *Engine) Show(id string, focus bool) error {
	s, err := e.Snapshot()
	if err != nil {
		return err
	}
	if ok, _ := e.Repair(s); ok {
		if s, err = e.Snapshot(); err != nil {
			return err
		}
	}
	target := s.Find(id)
	if target == nil || !target.IsAgent() {
		return fmt.Errorf("agent %s not found", id)
	}
	st, sb := s.Stage(), s.Sidebar()
	if st == nil || sb == nil {
		return errors.New("herd main session is not running")
	}
	var cmds [][]string
	if st.ID != target.ID {
		slot := st.ID
		if st.IsAgent() {
			ph, err := e.homeSlot(s, st)
			if err != nil {
				return err
			}
			// staged agent goes home, its placeholder comes to the stage
			cmds = append(cmds, []string{"swap-pane", "-d", "-s", st.ID, "-t", ph})
			slot = ph
		}
		cmds = append(cmds, []string{"swap-pane", "-d", "-s", target.ID, "-t", slot})
	}
	cmds = append(cmds, tmux.SetPaneOpt(target.ID, model.OptRead, "1"))
	if focus {
		cmds = append(cmds, []string{"select-pane", "-t", target.ID})
	} else {
		cmds = append(cmds, []string{"select-pane", "-t", sb.ID})
	}
	_, err = e.T.Batch(cmds...)
	return err
}

// Unstage sends the staged agent back to its own session.
func (e *Engine) Unstage() error {
	s, err := e.Snapshot()
	if err != nil {
		return err
	}
	return e.unstage(s)
}

func (e *Engine) unstage(s *Snapshot) error {
	st := s.StagedAgent()
	if st == nil {
		return nil
	}
	ph, err := e.homeSlot(s, st)
	if err != nil {
		return err
	}
	_, err = e.T.Run("swap-pane", "-d", "-s", st.ID, "-t", ph)
	return err
}

// ---- agents ---------------------------------------------------------------

// NewOpts describes a new agent.
type NewOpts struct {
	Kind     string
	Dir      string
	Name     string
	Prompt   string
	Group    string
	Worktree bool
}

// NewResult says what NewAgent made.
type NewResult struct {
	Pane    string
	Session string
	Note    string
}

// FromPreset fills NewOpts from a preset; explicit values in o win.
func FromPreset(c *config.Config, name string, o NewOpts) (NewOpts, error) {
	p, ok := c.Presets[name]
	if !ok {
		return o, fmt.Errorf("unknown preset %q", name)
	}
	if o.Kind == "" {
		o.Kind = p.Agent
	}
	if o.Dir == "" {
		o.Dir = p.Dir
	}
	if o.Name == "" {
		o.Name = p.Name
	}
	if o.Prompt == "" {
		o.Prompt = p.Prompt
	}
	if o.Group == "" {
		o.Group = p.Group
	}
	if p.Worktree != nil {
		o.Worktree = *p.Worktree
	} else {
		o.Worktree = c.Defaults.Worktree
	}
	return o, nil
}

// NewAgent starts an agent in its own session.
func (e *Engine) NewAgent(o NewOpts) (NewResult, error) {
	cfg := e.Cfg.Get()
	if o.Kind == "" {
		o.Kind = cfg.Defaults.Agent
	}
	ac, ok := cfg.Agents[o.Kind]
	if !ok {
		return NewResult{}, fmt.Errorf("unknown agent %q", o.Kind)
	}
	dir := config.Expand(strings.TrimSpace(o.Dir))
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	// git reports real paths (macOS: /var -> /private/var); match them
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return NewResult{}, fmt.Errorf("folder %s does not exist", dir)
	}
	s, err := e.Snapshot()
	if err != nil {
		return NewResult{}, err
	}
	slug := gitwt.Slug(o.Name)
	if strings.TrimSpace(o.Name) == "" {
		slug = gitwt.Slug(filepath.Base(dir))
		if o.Kind != cfg.Defaults.Agent {
			slug = gitwt.Slug(filepath.Base(dir) + "-" + o.Kind)
		}
	}
	session := "herd-" + slug
	for i := 2; s.HasSession(session) || e.T.HasSession(session); i++ {
		session = fmt.Sprintf("herd-%s-%d", slug, i)
	}
	title := strings.TrimSpace(o.Name)
	if title == "" {
		title = strings.TrimPrefix(session, "herd-")
	}

	res := NewResult{Session: session}
	workdir, repo, branch, base, wtPath := dir, gitwt.Toplevel(dir), "", "", ""
	if repo != "" {
		base = gitwt.Head(repo)
		branch = gitwt.Branch(repo)
		if o.Worktree && o.Kind != "shell" {
			wt, err := gitwt.Add(repo, cfg.WorktreeRoot(), cfg.Defaults.BranchPrefix, gitwt.Slug(strings.TrimPrefix(session, "herd-")))
			if err != nil {
				res.Note = "no worktree: " + err.Error()
			} else {
				rel, _ := filepath.Rel(repo, dir)
				workdir = wt.Path
				if rel != "." && rel != "" && !strings.HasPrefix(rel, "..") {
					workdir = filepath.Join(wt.Path, rel)
				}
				wtPath, branch, base = wt.Path, wt.Branch, wt.Base
			}
		}
	}

	args := []string{"new-session", "-d", "-s", session, "-c", workdir, "-P", "-F", "#{pane_id}"}
	if st := s.Stage(); st != nil && st.Width > 0 {
		args = append(args, "-x", strconv.Itoa(st.Width), "-y", strconv.Itoa(st.Height))
	}
	pane, err := e.T.Run(args...)
	if err != nil {
		return res, err
	}
	res.Pane = pane
	order := 0.0
	for _, a := range s.Agents() {
		if a.Order >= order {
			order = a.Order + 1
		}
	}
	cmds := [][]string{
		tmux.SetPaneOpt(pane, model.OptKind, o.Kind),
		tmux.SetPaneOpt(pane, model.OptTitle, title),
		tmux.SetPaneOpt(pane, model.OptHome, session),
		tmux.SetPaneOpt(pane, model.OptDir, dir),
		tmux.SetPaneOpt(pane, model.OptOrder, strconv.FormatFloat(order, 'f', -1, 64)),
		tmux.SetPaneOpt(pane, model.OptCreated, strconv.FormatInt(time.Now().Unix(), 10)),
		tmux.SetPaneOpt(pane, model.OptRead, "1"),
	}
	if o.Group != "" {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptGroup, o.Group))
	}
	if repo != "" {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptRepo, repo))
	}
	if wtPath != "" {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptWorktree, wtPath))
	}
	if branch != "" {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptBranch, branch))
	}
	if base != "" {
		cmds = append(cmds, tmux.SetPaneOpt(pane, model.OptBase, base))
	}
	if ac.Command != "" {
		line := ac.Command
		if strings.TrimSpace(o.Prompt) != "" {
			line += " " + tmux.Quote(o.Prompt)
		}
		cmds = append(cmds, []string{"send-keys", "-t", pane, "-l", line}, []string{"send-keys", "-t", pane, "Enter"})
	}
	if _, err := e.T.Batch(cmds...); err != nil {
		return res, err
	}
	if o.Group != "" {
		e.rememberGroup(s, o.Group)
	}
	return res, nil
}

// Close kills an agent's session. Its worktree stays.
func (e *Engine) Close(id string) error {
	s, err := e.Snapshot()
	if err != nil {
		return err
	}
	a := s.Find(id)
	if a == nil {
		return fmt.Errorf("agent %s not found", id)
	}
	if st := s.Stage(); st != nil && st.ID == a.ID {
		if err := e.unstage(s); err != nil {
			return err
		}
		// the placeholder is now on the stage; the agent is in its home session
	}
	if a.Home != "" && a.Home != s.Main {
		_, err = e.T.Run("kill-session", "-t", "="+a.Home)
		return err
	}
	_, err = e.T.Run("kill-pane", "-t", a.ID)
	return err
}

// RemoveWorktree closes the agent (if alive) and removes its worktree.
func (e *Engine) RemoveWorktree(a model.Pane, force bool) (gitwt.RemoveResult, error) {
	if a.Worktree == "" || a.Repo == "" {
		return gitwt.RemoveResult{}, errors.New("this agent has no worktree")
	}
	if !force && gitwt.Dirty(a.Worktree) {
		return gitwt.RemoveResult{}, errors.New("worktree has uncommitted changes")
	}
	if s, err := e.Snapshot(); err == nil && s.Find(a.ID) != nil {
		if err := e.Close(a.ID); err != nil {
			return gitwt.RemoveResult{}, err
		}
	}
	return gitwt.Remove(a.Repo, a.Worktree, a.Branch, force)
}

// SetTitle renames an agent.
func (e *Engine) SetTitle(a model.Pane, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("name cannot be empty")
	}
	if _, err := e.T.Run(tmux.SetPaneOpt(a.ID, model.OptTitle, title)...); err != nil {
		return err
	}
	e.script("rename", strconv.Itoa(a.PID), a.Group, title)
	return nil
}

// SetGroup moves an agent to a group ("" = no group).
func (e *Engine) SetGroup(a model.Pane, group string) error {
	group = strings.TrimSpace(group)
	var err error
	if group == "" {
		_, err = e.T.Run(tmux.UnsetPaneOpt(a.ID, model.OptGroup)...)
	} else {
		_, err = e.T.Run(tmux.SetPaneOpt(a.ID, model.OptGroup, group)...)
	}
	if err != nil {
		return err
	}
	if group != "" {
		if s, err := e.Snapshot(); err == nil {
			e.rememberGroup(s, group)
		}
	}
	e.script("group", a.SessionID, a.Group, group)
	return nil
}

func (e *Engine) rememberGroup(s *Snapshot, group string) {
	for _, g := range s.Groups {
		if g == group {
			return
		}
	}
	list := append(append([]string{}, s.Groups...), group)
	_, _ = e.T.Run("set-option", "-g", model.OptGroups, strings.Join(list, "\n"))
}

// MoveGroup moves a group one step up (-1) or down (+1) in the order.
func (e *Engine) MoveGroup(s *Snapshot, group string, delta int) error {
	order := s.GroupOrder()
	i := indexOf(order, group)
	j := i + delta
	if i < 0 || j < 0 || j >= len(order) {
		return nil
	}
	order[i], order[j] = order[j], order[i]
	_, err := e.T.Run("set-option", "-g", model.OptGroups, strings.Join(order, "\n"))
	return err
}

// ToggleFold collapses or expands a group.
func (e *Engine) ToggleFold(s *Snapshot, group string) error {
	var list []string
	found := false
	for g := range s.Folded {
		if g == group {
			found = true
			continue
		}
		list = append(list, g)
	}
	if !found {
		list = append(list, group)
	}
	sort.Strings(list)
	_, err := e.T.Run("set-option", "-g", model.OptCollapsed, strings.Join(list, "\n"))
	return err
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// Reorder swaps an agent with its neighbor in the same group.
func (e *Engine) Reorder(s *Snapshot, id string, delta int) error {
	ordered := s.Ordered()
	i := -1
	for k, a := range ordered {
		if a.ID == id {
			i = k
		}
	}
	if i < 0 {
		return nil
	}
	j := i + delta
	if j < 0 || j >= len(ordered) || ordered[j].Group != ordered[i].Group {
		return nil
	}
	// renumber the group so orders are unique, then swap
	var cmds [][]string
	var group []model.Pane
	for _, a := range ordered {
		if a.Group == ordered[i].Group {
			group = append(group, a)
		}
	}
	gi, gj := -1, -1
	for k, a := range group {
		if a.ID == ordered[i].ID {
			gi = k
		}
		if a.ID == ordered[j].ID {
			gj = k
		}
	}
	group[gi], group[gj] = group[gj], group[gi]
	for k, a := range group {
		cmds = append(cmds, tmux.SetPaneOpt(a.ID, model.OptOrder, strconv.Itoa(k)))
	}
	_, err := e.T.Batch(cmds...)
	return err
}

// SetFlag sets a boolean pane option.
func (e *Engine) SetFlag(id, opt string, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	_, err := e.T.Run(tmux.SetPaneOpt(id, opt, v)...)
	return err
}

// SendKeys sends tmux key names (Enter, Escape, y) to a pane.
func (e *Engine) SendKeys(id string, keys []string) error {
	if len(keys) == 0 {
		return errors.New("no keys configured")
	}
	_, err := e.T.Run(append([]string{"send-keys", "-t", id}, keys...)...)
	return err
}

// SendText types text into a pane and presses Enter.
func (e *Engine) SendText(id, text string) error {
	_, err := e.T.Batch(
		[]string{"send-keys", "-t", id, "-l", text},
		[]string{"send-keys", "-t", id, "Enter"},
	)
	return err
}

// Popup runs a shell script in a tmux popup in dir.
func (e *Engine) Popup(dir, script string) error {
	_, err := e.T.Run("display-popup", "-E", "-w", "90%", "-h", "90%", "-d", dir, "sh", "-c", script)
	return err
}

// Adopt takes over sessions marked with @herd_adopt 1.
func (e *Engine) Adopt(s *Snapshot) (int, error) {
	n := 0
	order := 0.0
	for _, a := range s.Agents() {
		if a.Order >= order {
			order = a.Order + 1
		}
	}
	done := map[string]bool{}
	for _, p := range s.Panes {
		if !p.AdoptFlag || p.Session == s.Main || done[p.Session] {
			continue
		}
		// pick the active pane of the active window
		if !p.Active || !p.WinActive || p.Kind != "" || p.Role != "" {
			continue
		}
		done[p.Session] = true
		kind := "shell"
		switch {
		case strings.Contains(p.Cmd, "claude"):
			kind = "claude"
		case strings.Contains(p.Cmd, "codex"):
			kind = "codex"
		}
		cmds := [][]string{
			tmux.SetPaneOpt(p.ID, model.OptKind, kind),
			tmux.SetPaneOpt(p.ID, model.OptTitle, p.Session),
			tmux.SetPaneOpt(p.ID, model.OptHome, p.Session),
			tmux.SetPaneOpt(p.ID, model.OptDir, p.Path),
			tmux.SetPaneOpt(p.ID, model.OptOrder, strconv.FormatFloat(order, 'f', -1, 64)),
			tmux.SetPaneOpt(p.ID, model.OptCreated, strconv.FormatInt(time.Now().Unix()-int64(detectGrace()), 10)),
			tmux.SetPaneOpt(p.ID, model.OptRead, "0"),
			{"set-option", "-u", "-t", "=" + p.Session + ":", model.OptAdopt},
		}
		if repo := gitwt.Toplevel(p.Path); repo != "" {
			cmds = append(cmds, tmux.SetPaneOpt(p.ID, model.OptRepo, repo), tmux.SetPaneOpt(p.ID, model.OptBase, gitwt.Head(repo)))
		}
		if _, err := e.T.Batch(cmds...); err != nil {
			return n, err
		}
		order++
		n++
	}
	return n, nil
}

func detectGrace() int { return 10 }

// NextAttention picks the next agent to look at after current: agents that
// need you first, then unread finished ones. dir is +1 or -1.
func NextAttention(ordered []model.Pane, current string, dir int) string {
	if len(ordered) == 0 {
		return ""
	}
	start := -1
	for i, a := range ordered {
		if a.ID == current {
			start = i
		}
	}
	pick := func(match func(model.Pane) bool) string {
		n := len(ordered)
		for k := 1; k <= n; k++ {
			i := ((start+dir*k)%n + n) % n
			if start < 0 && dir > 0 {
				i = (k - 1) % n
			}
			a := ordered[i]
			if a.ID != current && !a.Inactive && match(a) {
				return a.ID
			}
		}
		return ""
	}
	if id := pick(func(a model.Pane) bool { return a.View == model.NeedsYou }); id != "" {
		return id
	}
	return pick(func(a model.Pane) bool { return !a.Read && a.View.Attention() })
}

// Counts is a summary for the status line.
type Counts struct {
	NeedsYou, Working, Unread, Total int
}

// Count summarizes agents.
func Count(agents []model.Pane) Counts {
	var c Counts
	for _, a := range agents {
		c.Total++
		if a.Inactive {
			continue
		}
		switch {
		case a.View == model.NeedsYou:
			c.NeedsYou++
		case a.View.Busy():
			c.Working++
		case !a.Read && a.View.Attention():
			c.Unread++
		}
	}
	return c
}

// Quit sends the staged agent home and kills the main session. Agents keep running.
func (e *Engine) Quit() error {
	s, err := e.Snapshot()
	if err != nil {
		return err
	}
	if !s.HasSession(s.Main) {
		return nil
	}
	if err := e.unstage(s); err != nil {
		return err
	}
	_, err = e.T.Run("kill-session", "-t", "="+s.Main)
	return err
}

// script calls the session script, if configured.
func (e *Engine) script(args ...string) {
	sc := e.Cfg.Get().Integrations.Tmux.SessionScript
	if sc == "" {
		return
	}
	cmd := execCommand(config.Expand(sc), args...)
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
}
