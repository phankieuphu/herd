package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/hook"
	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/tmux"
)

func TestNextAttention(t *testing.T) {
	ps := []model.Pane{
		{ID: "%1", View: model.Working, Read: true},
		{ID: "%2", View: model.Done, Read: false},
		{ID: "%3", View: model.NeedsYou, Read: true},
		{ID: "%4", View: model.NeedsYou, Read: true, Inactive: true},
		{ID: "%5", View: model.Done, Read: true},
	}
	if got := NextAttention(ps, "%1", 1); got != "%3" {
		t.Fatalf("needs you first, got %s", got)
	}
	if got := NextAttention(ps, "%3", 1); got != "%2" {
		t.Fatalf("then unread, got %s", got)
	}
	if got := NextAttention(ps, "", 1); got != "%3" {
		t.Fatalf("no current, got %s", got)
	}
	if got := NextAttention(ps[:1], "%1", 1); got != "" {
		t.Fatalf("nothing to do, got %s", got)
	}
	c := Count(ps)
	if c.NeedsYou != 1 || c.Working != 1 || c.Unread != 1 || c.Total != 5 {
		t.Fatalf("%+v", c)
	}
}

func TestOrdered(t *testing.T) {
	s := &Snapshot{Groups: []string{"b", "a"}, Panes: []model.Pane{
		{ID: "%1", Kind: "shell", Group: "a", Order: 1},
		{ID: "%2", Kind: "shell", Group: "", Order: 5},
		{ID: "%3", Kind: "shell", Group: "b", Order: 2},
		{ID: "%4", Kind: "shell", Group: "a", Order: 0},
		{ID: "%5", Kind: "shell", Group: "c", Order: 0},
		{ID: "%6", Role: "placeholder", Kind: ""},
	}}
	var ids []string
	for _, a := range s.Ordered() {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, " ") != "%2 %3 %4 %1 %5" {
		t.Fatalf("order %v", ids)
	}
	if g := s.GroupOrder(); strings.Join(g, ",") != "b,a,c" {
		t.Fatalf("groups %v", g)
	}
}

// ---- integration with a real tmux server ----------------------------------

type harness struct {
	t   *testing.T
	eng *Engine
	dir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	t.Setenv("HERD_HOME", filepath.Join(dir, "home"))
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("TMUX", "")
	// a fake herd binary: sidebar and placeholder just wait
	self := filepath.Join(dir, "fakeherd")
	if err := os.WriteFile(self, []byte("#!/bin/sh\nexec sleep 100000\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Defaults.WorktreeDir = filepath.Join(dir, "worktrees")
	cfg.Integrations.Tmux.BindKeys = false
	socket := fmt.Sprintf("herdtest-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Setenv("HERD_TMUX_SOCKET", socket)
	eng := &Engine{T: &tmux.Client{Bin: "tmux", Socket: socket}, Cfg: config.NewStore(cfg), Self: self}
	t.Cleanup(func() { _, _ = eng.T.Run("kill-server") })
	return &harness{t: t, eng: eng, dir: dir}
}

func (h *harness) snap() *Snapshot {
	h.t.Helper()
	s, err := h.eng.Snapshot()
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) newAgent(name string, o NewOpts) string {
	h.t.Helper()
	o.Name = name
	if o.Kind == "" {
		o.Kind = "shell"
	}
	if o.Dir == "" {
		o.Dir = h.dir
	}
	res, err := h.eng.NewAgent(o)
	h.must(err)
	return res.Pane
}

func TestLayoutAndStage(t *testing.T) {
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	h.must(h.eng.Create(160, 40, h.dir)) // second call is a no-op

	s := h.snap()
	sb, st := s.Sidebar(), s.Stage()
	if sb == nil || st == nil || st.Role != model.RolePlaceholder {
		t.Fatalf("layout: sidebar=%v stage=%v", sb, st)
	}
	if sb.Width != 34 {
		t.Fatalf("sidebar width %d", sb.Width)
	}

	one := h.newAgent("one", NewOpts{})
	two := h.newAgent("Two words", NewOpts{Group: "grp"})
	s = h.snap()
	a1, a2 := s.Find(one), s.Find(two)
	if a1 == nil || a1.Session != "herd-one" || a1.Home != "herd-one" || a1.Kind != "shell" || a1.Title != "one" {
		t.Fatalf("agent one: %+v", a1)
	}
	if a2.Session != "herd-two-words" || a2.Group != "grp" || a2.Order <= a1.Order {
		t.Fatalf("agent two: %+v", a2)
	}
	if len(s.Groups) != 1 || s.Groups[0] != "grp" {
		t.Fatalf("groups %v", s.Groups)
	}

	// show one: it moves to the stage, a placeholder keeps herd-one alive
	h.must(h.eng.Show(one, true))
	s = h.snap()
	if s.Stage().ID != one || s.Find(one).Session != s.Main {
		t.Fatalf("one not on stage")
	}
	if ph := s.placeholderIn("herd-one"); ph == nil {
		t.Fatal("no placeholder in herd-one")
	}

	// show two: one goes home, two comes in
	h.must(h.eng.Show(two, false))
	s = h.snap()
	if s.Stage().ID != two || s.Find(one).Session != "herd-one" || s.placeholderIn("herd-two-words") == nil {
		t.Fatalf("swap failed: stage=%s one in %s", s.Stage().ID, s.Find(one).Session)
	}
	if s.placeholderIn("herd-one") != nil {
		t.Fatal("placeholder should have moved out of herd-one")
	}
	// showing the staged agent again changes nothing
	h.must(h.eng.Show(two, false))
	if h.snap().Stage().ID != two {
		t.Fatal("re-show")
	}

	// close the staged agent: stage gets a placeholder, the session is gone
	h.must(h.eng.Close(two))
	s = h.snap()
	if s.Stage() == nil || s.Stage().Role != model.RolePlaceholder || s.HasSession("herd-two-words") {
		t.Fatalf("close: stage=%+v", s.Stage())
	}
	if len(s.Agents()) != 1 {
		t.Fatalf("agents %d", len(s.Agents()))
	}

	// an agent that dies on the stage: Repair restores the layout and
	// removes the leftover session that only holds a placeholder
	h.must(h.eng.Show(one, false))
	_, err := h.eng.T.Run("kill-pane", "-t", one)
	h.must(err)
	s = h.snap()
	if s.Stage() != nil {
		t.Fatal("expected a broken layout")
	}
	changed, err := h.eng.Repair(s)
	h.must(err)
	if !changed {
		t.Fatal("repair did nothing")
	}
	s = h.snap()
	if s.Stage() == nil || s.Stage().Role != model.RolePlaceholder || s.HasSession("herd-one") {
		t.Fatalf("after repair: stage=%v herd-one=%v", s.Stage(), s.HasSession("herd-one"))
	}

	// sidebar killed: Repair brings it back
	_, err = h.eng.T.Run("kill-pane", "-t", s.Sidebar().ID)
	h.must(err)
	_, err = h.eng.Repair(h.snap())
	h.must(err)
	if h.snap().Sidebar() == nil {
		t.Fatal("sidebar not restored")
	}

	// quit keeps agents alive
	three := h.newAgent("three", NewOpts{})
	h.must(h.eng.Show(three, false))
	h.must(h.eng.Quit())
	s = h.snap()
	if s.HasSession(s.Main) || s.Find(three) == nil || s.Find(three).Session != "herd-three" {
		t.Fatal("quit should send the agent home and keep it")
	}
}

func TestShowWithoutHomeSession(t *testing.T) {
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	one := h.newAgent("one", NewOpts{})
	two := h.newAgent("two", NewOpts{})
	h.must(h.eng.Show(one, false))
	// someone killed the placeholder that held herd-one
	ph := h.snap().placeholderIn("herd-one")
	_, err := h.eng.T.Run("kill-pane", "-t", ph.ID)
	h.must(err)
	// showing two must recreate a home for one instead of losing it
	h.must(h.eng.Show(two, false))
	s := h.snap()
	if s.Find(one) == nil || s.Find(one).Session != "herd-one" || s.Stage().ID != two {
		t.Fatalf("one=%+v", s.Find(one))
	}
}

func TestEditsAndAdopt(t *testing.T) {
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	a := h.newAgent("a", NewOpts{})
	b := h.newAgent("b", NewOpts{})
	c := h.newAgent("c", NewOpts{})
	s := h.snap()

	h.must(h.eng.SetTitle(*s.Find(a), "alpha"))
	h.must(h.eng.SetGroup(*s.Find(b), "g1"))
	h.must(h.eng.SetGroup(*s.Find(c), "g1"))
	h.must(h.eng.SetFlag(a, model.OptInactive, true))
	s = h.snap()
	if s.Find(a).Name() != "alpha" || !s.Find(a).Inactive || s.Find(b).Group != "g1" {
		t.Fatal("edits not stored")
	}
	h.must(h.eng.Reorder(s, c, -1))
	s = h.snap()
	var ids []string
	for _, x := range s.Ordered() {
		ids = append(ids, x.ID)
	}
	if strings.Join(ids, " ") != strings.Join([]string{a, c, b}, " ") {
		t.Fatalf("reorder: %v", ids)
	}
	h.must(h.eng.ToggleFold(s, "g1"))
	if !h.snap().Folded["g1"] {
		t.Fatal("fold")
	}
	h.must(h.eng.SetGroup(*s.Find(b), ""))
	if h.snap().Find(b).Group != "" {
		t.Fatal("ungroup")
	}

	// adopt a session started outside herd (Harp's option names)
	_, err := h.eng.T.Run("new-session", "-d", "-s", "phone", "-c", h.dir)
	h.must(err)
	_, err = h.eng.T.Batch(
		[]string{"set-option", "-t", "=phone:", model.OptWarpAdopt, "1"},
		[]string{"set-option", "-t", "=phone:", model.OptWarpGroup, "remote"},
	)
	h.must(err)
	n, err := h.eng.Adopt(h.snap())
	h.must(err)
	if n != 1 {
		t.Fatalf("adopted %d", n)
	}
	var adopted *model.Pane
	for _, x := range h.snap().Agents() {
		if x.Session == "phone" {
			x := x
			adopted = &x
		}
	}
	if adopted == nil || adopted.Group != "remote" || adopted.Home != "phone" || adopted.Read {
		t.Fatalf("adopted %+v", adopted)
	}
	if n, _ := h.eng.Adopt(h.snap()); n != 0 {
		t.Fatal("adopted twice")
	}
	// adopted agents can be shown and sent home like any other
	h.must(h.eng.Show(adopted.ID, false))
	h.must(h.eng.Unstage())
	if h.snap().Find(adopted.ID).Session != "phone" {
		t.Fatal("adopted agent did not go home")
	}
}

func TestSendAndCapture(t *testing.T) {
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	a := h.newAgent("a", NewOpts{})
	h.must(h.eng.SendText(a, "echo herd-says-hi"))
	var got string
	for i := 0; i < 40; i++ {
		got = h.eng.Capture([]string{a, "%9999"})[a]
		if strings.Count(got, "herd-says-hi") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Count(got, "herd-says-hi") < 2 {
		t.Fatalf("capture: %q", got)
	}
}

func TestHookWritesPane(t *testing.T) {
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	a := h.newAgent("a", NewOpts{})
	t.Setenv("TMUX_PANE", a)
	tr := filepath.Join(h.dir, "t.jsonl")
	os.WriteFile(tr, []byte(""), 0o644)
	hook.RunClaude(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","transcript_path":"` + tr + `"}`))
	p := h.snap().Find(a)
	if !p.Hooked || p.HookState != model.Working || p.Transcript != tr {
		t.Fatalf("after prompt: %+v", p)
	}
	hook.RunClaude(strings.NewReader(`{"hook_event_name":"Stop","transcript_path":"` + tr + `"}`))
	p = h.snap().Find(a)
	if p.HookState != model.Done || p.Read || p.BG != 0 {
		t.Fatalf("after stop: %+v", p)
	}
	hook.RunCodex([]string{`{"type":"agent-turn-complete"}`})
	// bad input must not panic or write
	hook.RunClaude(strings.NewReader(`{nope`))
}

func TestNewAgentWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	realDir, _ := filepath.EvalSymlinks(h.dir)
	repo := filepath.Join(realDir, "api")
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	gitc := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	gitc("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "sub", "x"), []byte("x"), 0o644)
	gitc("add", ".")
	gitc("commit", "-q", "-m", "i")

	res, err := h.eng.NewAgent(NewOpts{Kind: "claude", Dir: filepath.Join(repo, "sub"), Name: "Fix auth", Worktree: true})
	h.must(err)
	p := h.snap().Find(res.Pane)
	if p.Branch != "herd/fix-auth" || p.Repo != repo || p.Worktree == "" || p.Base == "" {
		t.Fatalf("%+v note=%s", p, res.Note)
	}
	// the pane starts in the same sub folder inside the worktree
	want := filepath.Join(p.Worktree, "sub")
	realPath := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	for i := 0; i < 40 && realPath(h.snap().Find(res.Pane).Path) != realPath(want); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if got := h.snap().Find(res.Pane).Path; realPath(got) != realPath(want) {
		t.Fatalf("path %s want %s", got, want)
	}
	// claude is not installed here: the typed command fails and the shell stays
	rr, err := h.eng.RemoveWorktree(*p, false)
	h.must(err)
	if !rr.BranchDeleted {
		t.Fatalf("%+v", rr)
	}
	if h.snap().Find(res.Pane) != nil {
		t.Fatal("agent should be closed")
	}
	if _, err := os.Stat(p.Worktree); !os.IsNotExist(err) {
		t.Fatal("worktree folder still there")
	}

	// no worktree when asked not to, and for plain folders
	res, err = h.eng.NewAgent(NewOpts{Kind: "codex", Dir: repo, Worktree: false})
	h.must(err)
	p = h.snap().Find(res.Pane)
	if p.Worktree != "" || p.Branch != "main" || p.Title != "api-codex" {
		t.Fatalf("%+v", p)
	}
	if _, err := h.eng.NewAgent(NewOpts{Kind: "nope", Dir: repo}); err == nil {
		t.Fatal("unknown agent")
	}
	if _, err := h.eng.NewAgent(NewOpts{Kind: "shell", Dir: filepath.Join(h.dir, "missing")}); err == nil {
		t.Fatal("missing folder")
	}
}

func TestTrackerTransitions(t *testing.T) {
	h := newHarness(t)
	h.must(h.eng.Create(160, 40, h.dir))
	a := h.newAgent("a", NewOpts{})
	tr := NewTracker()
	tr.Grace = 0
	cfg := h.eng.Cfg.Get()

	step := func() Result {
		s := h.snap()
		ids := []string{}
		for _, x := range s.Agents() {
			ids = append(ids, x.ID)
		}
		res := tr.Update(cfg, s, h.eng.Capture(ids), time.Now())
		_, err := h.eng.T.Batch(res.Writes...)
		h.must(err)
		return res
	}
	for i := 0; i < 50 && !model.IsShell(h.snap().Find(a).Cmd); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if r := step(); r.Shown[a] != model.Done || len(r.Transitions) != 0 {
		t.Fatalf("idle shell: %+v", r)
	}
	h.must(h.eng.SendText(a, "sleep 1"))
	var working bool
	for i := 0; i < 20 && !working; i++ {
		time.Sleep(100 * time.Millisecond)
		working = step().Shown[a] == model.Working
	}
	if !working {
		t.Fatal("never saw working")
	}
	var done Result
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		if done = step(); done.Shown[a] == model.Done {
			break
		}
	}
	if done.Shown[a] != model.Done || len(done.Transitions) != 1 || done.Transitions[0].To != model.Done || done.Transitions[0].Seen {
		t.Fatalf("finish: %+v", done)
	}
	p := h.snap().Find(a)
	if p.Read || p.View != model.Done {
		t.Fatalf("unread mark / view not written: %+v", p)
	}
	// a new tracker (sidebar restart) continues without a fake transition
	tr2 := NewTracker()
	s := h.snap()
	if r := tr2.Update(cfg, s, h.eng.Capture([]string{a}), time.Now()); len(r.Transitions) != 0 {
		t.Fatalf("restart produced %v", r.Transitions)
	}
}
