// Command herd runs many Claude Code and Codex agents side by side in tmux,
// with a sidebar that tells you which one needs you.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/core"
	"github.com/phankieuphu/herd/internal/detect"
	"github.com/phankieuphu/herd/internal/hook"
	"github.com/phankieuphu/herd/internal/install"
	"github.com/phankieuphu/herd/internal/limits"
	"github.com/phankieuphu/herd/internal/model"
	"github.com/phankieuphu/herd/internal/notify"
	"github.com/phankieuphu/herd/internal/selfupdate"
	"github.com/phankieuphu/herd/internal/tmux"
	"github.com/phankieuphu/herd/internal/ui"
)

var version = "dev"

const usage = `herd — run many coding agents side by side in tmux.

Usage:
  herd                      open the herd window (start it if needed)
  herd new [flags]          start an agent
  herd ls                   list agents
  herd attach [name]        attach to an agent's own session (good over ssh)
  herd show <name>          show an agent in the herd window
  herd jump                 show the next agent that needs you
  herd status [--tmux|--json]
                            short summary for a status bar or scripts
  herd install-hooks        add herd hooks to Claude Code (and Codex)
  herd uninstall-hooks      remove them again
  herd init-config          write a commented ~/.herd/settings.toml
  herd doctor               check your setup
  herd quit                 close the herd window (agents keep running)
  herd update [ref]         update to the latest release, or build a branch,
                            tag or commit (needs Go), e.g. herd update main
  herd version

Run 'herd new -h' for the flags of new.
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	// hooks run inside agents: never fail, never print, never need config
	if cmd == "hook" {
		runHook(args)
		return
	}
	if err := run(cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, "herd:", err)
		os.Exit(1)
	}
}

func runHook(args []string) {
	defer func() { _ = recover() }()
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "claude":
		hook.RunClaude(os.Stdin)
	case "codex":
		hook.RunCodex(args[1:])
	}
}

func loadEngine() (*core.Engine, *config.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	st := config.NewStore(cfg)
	return core.New(st), st, nil
}

func run(cmd string, args []string) error {
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("herd", currentVersion())
		return nil
	case "update", "upgrade":
		return cmdUpdate(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "init-config":
		return initConfig()
	}
	if _, err := exec.LookPath("tmux"); err != nil && cmd != "doctor" {
		return errors.New("tmux is not installed (brew install tmux / apt install tmux)")
	}
	eng, cfg, err := loadEngine()
	if err != nil {
		if cmd == "placeholder" || cmd == "sidebar" {
			// still start, with defaults, so the window is usable
			cfg = config.NewStore(config.Default())
			eng = core.New(cfg)
		} else {
			return err
		}
	}
	switch cmd {
	case "", "up", "open":
		return up(eng)
	case "sidebar":
		return ui.Run(eng, cfg)
	case "placeholder":
		runPlaceholder(cfg.Get())
		return nil
	case "new":
		return cmdNew(eng, args)
	case "ls", "list":
		return cmdList(eng)
	case "attach", "a":
		return cmdAttach(eng, args)
	case "show":
		return cmdShow(eng, args)
	case "jump":
		return cmdJump(eng)
	case "focus-sidebar":
		s, err := eng.Snapshot()
		if err != nil {
			return err
		}
		if sb := s.Sidebar(); sb != nil {
			_, err = eng.T.Run("select-pane", "-t", sb.ID)
		}
		return err
	case "status":
		return cmdStatus(eng, args)
	case "install-hooks":
		return cmdInstall(eng, args)
	case "uninstall-hooks":
		return cmdUninstall()
	case "doctor":
		return cmdDoctor(eng, cfg.Get())
	case "quit":
		return eng.Quit()
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func checkTmuxVersion(t *tmux.Client) error {
	ma, mi, err := t.Version()
	if err != nil {
		return err
	}
	if ma < 3 || (ma == 3 && mi < 2) {
		return fmt.Errorf("herd needs tmux 3.2 or newer (you have %d.%d)", ma, mi)
	}
	return nil
}

func up(eng *core.Engine) error {
	if err := checkTmuxVersion(eng.T); err != nil {
		return err
	}
	main := eng.Main()
	if !eng.T.HasSession(main) {
		cols, rows, _ := term.GetSize(os.Stdout.Fd())
		cwd, _ := os.Getwd()
		if err := eng.Create(cols, rows, cwd); err != nil {
			return err
		}
	} else if s, err := eng.Snapshot(); err == nil {
		_, _ = eng.Repair(s)
		eng.BindKeys()
	}
	return attachSession(eng.T, main)
}

func attachSession(t *tmux.Client, session string) error {
	if os.Getenv("TMUX") != "" {
		_, err := t.Run("switch-client", "-t", "="+session)
		return err
	}
	bin, err := exec.LookPath(t.Bin)
	if err != nil {
		return err
	}
	argv := []string{t.Bin}
	if t.Socket != "" {
		argv = append(argv, "-L", t.Socket)
	}
	argv = append(argv, "attach-session", "-t", "="+session)
	return syscall.Exec(bin, argv, os.Environ())
}

func cmdNew(eng *core.Engine, args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	cfg := eng.Cfg.Get()
	kind := fs.String("a", "", "agent: "+strings.Join(cfg.AgentNames(), ", ")+" (default "+cfg.Defaults.Agent+")")
	dir := fs.String("d", "", "folder to work in (default: current folder)")
	name := fs.String("n", "", "name shown in the sidebar")
	prompt := fs.String("p", "", "first prompt for the agent")
	group := fs.String("g", "", "group")
	preset := fs.String("preset", "", "start from a preset in settings.toml")
	noWT := fs.Bool("no-worktree", false, "work in the folder itself, not in a new git worktree")
	wt := fs.Bool("worktree", false, "force a new git worktree")
	show := fs.Bool("show", false, "show it in the herd window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	o := core.NewOpts{Kind: *kind, Dir: *dir, Name: *name, Prompt: *prompt, Group: *group, Worktree: cfg.Defaults.Worktree}
	if *preset != "" {
		var err error
		if o, err = core.FromPreset(cfg, *preset, o); err != nil {
			return err
		}
	}
	if *noWT {
		o.Worktree = false
	}
	if *wt {
		o.Worktree = true
	}
	if o.Prompt == "" && fs.NArg() > 0 {
		o.Prompt = strings.Join(fs.Args(), " ")
	}
	res, err := eng.NewAgent(o)
	if err != nil {
		return err
	}
	fmt.Printf("started %s (session %s)\n", strings.TrimPrefix(res.Session, "herd-"), res.Session)
	if res.Note != "" {
		fmt.Println("note:", res.Note)
	}
	if *show && eng.T.HasSession(eng.Main()) {
		return eng.Show(res.Pane, true)
	}
	return nil
}

func findAgent(s *core.Snapshot, name string) (*model.Pane, error) {
	agents := s.Ordered()
	name = strings.TrimSpace(name)
	match := func(f func(model.Pane) bool) []model.Pane {
		var out []model.Pane
		for _, a := range agents {
			if f(a) {
				out = append(out, a)
			}
		}
		return out
	}
	lower := strings.ToLower(name)
	tries := []func(model.Pane) bool{
		func(a model.Pane) bool { return a.ID == name || a.Home == name || a.Name() == name },
		func(a model.Pane) bool {
			return strings.ToLower(a.Name()) == lower || strings.TrimPrefix(a.Home, "herd-") == lower
		},
		func(a model.Pane) bool {
			return strings.HasPrefix(strings.ToLower(a.Name()), lower) || strings.HasPrefix(strings.TrimPrefix(a.Home, "herd-"), lower)
		},
		func(a model.Pane) bool { return strings.Contains(strings.ToLower(a.Name()), lower) },
	}
	for _, f := range tries {
		if m := match(f); len(m) == 1 {
			return &m[0], nil
		} else if len(m) > 1 {
			var names []string
			for _, a := range m {
				names = append(names, a.Name())
			}
			return nil, fmt.Errorf("%q matches several agents: %s", name, strings.Join(names, ", "))
		}
	}
	return nil, fmt.Errorf("no agent called %q (see herd ls)", name)
}

func cmdAttach(eng *core.Engine, args []string) error {
	if len(args) == 0 {
		return up(eng)
	}
	s, err := eng.Snapshot()
	if err != nil {
		return err
	}
	a, err := findAgent(s, strings.Join(args, " "))
	if err != nil {
		return err
	}
	if st := s.StagedAgent(); st != nil && st.ID == a.ID {
		if err := eng.Unstage(); err != nil {
			return err
		}
	}
	if s2, err := eng.Snapshot(); err == nil {
		if p := s2.Find(a.ID); p != nil {
			a = p
		}
	}
	_, _ = eng.T.Run("set-option", "-p", "-t", a.ID, model.OptRead, "1")
	return attachSession(eng.T, a.Session)
}

func cmdShow(eng *core.Engine, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: herd show <name>")
	}
	s, err := eng.Snapshot()
	if err != nil {
		return err
	}
	a, err := findAgent(s, strings.Join(args, " "))
	if err != nil {
		return err
	}
	return eng.Show(a.ID, true)
}

func cmdJump(eng *core.Engine) error {
	s, err := eng.Snapshot()
	if err != nil {
		return err
	}
	cur := ""
	if st := s.StagedAgent(); st != nil {
		cur = st.ID
	}
	id := core.NextAttention(s.Ordered(), cur, 1)
	if id == "" {
		_, _ = eng.T.Run("display-message", "herd: no agent needs you")
		return nil
	}
	return eng.Show(id, true)
}

func cmdList(eng *core.Engine) error {
	s, err := eng.Snapshot()
	if err != nil {
		return err
	}
	agents := s.Ordered()
	if len(agents) == 0 {
		fmt.Println("no agents. start one with: herd new")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tNAME\tAGENT\tGROUP\tBRANCH\tSESSION")
	stage := ""
	if st := s.StagedAgent(); st != nil {
		stage = st.ID
	}
	for _, a := range agents {
		state := string(a.View)
		if !a.Read && a.View.Attention() {
			state += "*"
		}
		if a.Inactive {
			state = "aside"
		}
		name := a.Name()
		if a.ID == stage {
			name += " (shown)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", state, name, a.Kind, dash(a.Group), dash(a.Branch), a.Home)
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type statusJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Agent    string `json:"agent"`
	Group    string `json:"group,omitempty"`
	State    string `json:"state"`
	Unread   bool   `json:"unread"`
	Aside    bool   `json:"aside"`
	Branch   string `json:"branch,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Session  string `json:"session"`
	Shown    bool   `json:"shown"`
}

func cmdStatus(eng *core.Engine, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	asTmux := fs.Bool("tmux", false, "print with tmux colors for status-right")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := eng.Snapshot()
	if err != nil {
		return err
	}
	agents := s.Ordered()
	if *asJSON {
		stage := ""
		if st := s.StagedAgent(); st != nil {
			stage = st.ID
		}
		list := []statusJSON{}
		for _, a := range agents {
			list = append(list, statusJSON{ID: a.ID, Name: a.Name(), Agent: a.Kind, Group: a.Group,
				State: string(a.View), Unread: !a.Read && a.View.Attention(), Aside: a.Inactive,
				Branch: a.Branch, Worktree: a.Worktree, Session: a.Home, Shown: a.ID == stage})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	fmt.Println(StatusLine(core.Count(agents), *asTmux))
	return nil
}

// StatusLine renders counts like "◆2 ●3 ✓1".
func StatusLine(c core.Counts, tmuxColors bool) string {
	var parts []string
	add := func(n int, sym, color string) {
		if n == 0 {
			return
		}
		if tmuxColors {
			parts = append(parts, fmt.Sprintf("#[fg=%s,bold]%s%d#[default]", color, sym, n))
		} else {
			parts = append(parts, fmt.Sprintf("%s%d", sym, n))
		}
	}
	add(c.NeedsYou, "◆", "red")
	add(c.Working, "●", "yellow")
	add(c.Unread, "✓", "green")
	return strings.Join(parts, " ")
}

func cmdInstall(eng *core.Engine, args []string) error {
	fs := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	_, codexErr := exec.LookPath("codex")
	claude := fs.Bool("claude", true, "add hooks to Claude Code settings")
	codex := fs.Bool("codex", codexErr == nil, "set notify in Codex config")
	dry := fs.Bool("dry-run", false, "print the result instead of writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *claude {
		path := install.ClaudeSettingsPath()
		data, _ := os.ReadFile(path)
		out, changed, err := install.MergeClaude(data, eng.Self)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		switch {
		case *dry:
			fmt.Printf("# %s\n%s\n", path, out)
		case !changed:
			fmt.Println("Claude Code: hooks already installed in", path)
		default:
			if err := install.WriteWithBackup(path, out); err != nil {
				return err
			}
			fmt.Println("Claude Code: hooks added to", path, "(old file kept as settings.json.herd-bak)")
			fmt.Println("  restart running claude sessions to pick them up")
		}
	}
	if *codex {
		path := install.CodexConfigPath()
		data, _ := os.ReadFile(path)
		out, changed, err := install.MergeCodex(data, eng.Self)
		switch {
		case err != nil:
			fmt.Println("Codex:", err)
		case *dry:
			fmt.Printf("# %s\n%s\n", path, out)
		case !changed:
			fmt.Println("Codex: notify already set in", path)
		default:
			if err := install.WriteWithBackup(path, out); err != nil {
				return err
			}
			fmt.Println("Codex: notify added to", path)
		}
	}
	return nil
}

func cmdUninstall() error {
	path := install.ClaudeSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("Claude Code: no settings file")
		return nil
	}
	out, changed, err := install.RemoveClaude(data)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println("Claude Code: no herd hooks found")
	} else if err := install.WriteWithBackup(path, out); err != nil {
		return err
	} else {
		fmt.Println("Claude Code: herd hooks removed from", path)
	}
	fmt.Println("Codex: remove the `notify = [...herd...]` line from", install.CodexConfigPath(), "by hand if you added it")
	return nil
}

func initConfig() error {
	path := config.Path()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(config.Example), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func cmdDoctor(eng *core.Engine, cfg *config.Config) error {
	ok := func(good bool, msg string) {
		mark := "ok  "
		if !good {
			mark = "warn"
		}
		fmt.Printf("[%s] %s\n", mark, msg)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		ok(false, "tmux not found")
	} else if err := checkTmuxVersion(eng.T); err != nil {
		ok(false, err.Error())
	} else {
		ma, mi, _ := eng.T.Version()
		ok(true, fmt.Sprintf("tmux %d.%d", ma, mi))
	}
	_, gitErr := exec.LookPath("git")
	ok(gitErr == nil, "git (for worktrees and diffs)")
	for _, name := range cfg.AgentNames() {
		a := cfg.Agents[name]
		if a.Command == "" {
			continue
		}
		bin := strings.Fields(a.Command)[0]
		_, err := exec.LookPath(config.Expand(bin))
		ok(err == nil, fmt.Sprintf("agent %s: %s", name, map[bool]string{true: "found " + bin, false: bin + " not in PATH"}[err == nil]))
		if bad := detect.BadPatterns(a); len(bad) > 0 {
			ok(false, fmt.Sprintf("agent %s: bad patterns %q", name, bad))
		}
	}
	if _, err := os.Stat(config.Path()); err == nil {
		ok(true, "settings: "+config.Path())
	} else {
		ok(true, "settings: defaults (run herd init-config to write a file)")
	}
	ok(install.ClaudeInstalled(), "Claude Code hooks (herd install-hooks)")
	if _, err := exec.LookPath("codex"); err == nil {
		ok(install.CodexInstalled(), "Codex notify (herd install-hooks --codex)")
	}
	if cfg.Notify.Desktop && !notify.HasClickAction() {
		ok(false, "desktop notifications: brew install terminal-notifier, so clicking one opens your terminal (now it opens Script Editor)")
	}
	if lr := cfg.Integrations.LimitRings; lr.Enabled {
		r := limits.Reader{Path: config.Expand(lr.File)}
		ps, err := r.Read(time.Now())
		ok(err == nil, fmt.Sprintf("limit rings: %s (%d providers)", lr.File, len(ps)))
	}
	if wt := cfg.WorktreeRoot(); cfg.Defaults.Worktree {
		ok(true, "worktrees go to "+wt)
	}
	if s, err := eng.Snapshot(); err == nil && s.HasSession(s.Main) {
		ok(s.Sidebar() != nil && s.Stage() != nil, fmt.Sprintf("herd window %q running with %d agent(s)", s.Main, len(s.Agents())))
	} else {
		ok(true, "herd window not running (run herd)")
	}
	return nil
}

// currentVersion is the version set at build time, or for go install
// builds the module version Go recorded.
func currentVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func cmdUpdate(args []string) error {
	ref := "latest"
	if len(args) > 0 {
		ref = args[0]
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	cur := currentVersion()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if ref == "latest" {
		tag, err := selfupdate.Latest(ctx)
		if err != nil {
			return err
		}
		if selfupdate.Same(cur, tag) {
			fmt.Println("herd is up to date:", tag)
			return nil
		}
		fmt.Printf("downloading herd %s for %s/%s ...\n", tag, runtime.GOOS, runtime.GOARCH)
		bin, err := selfupdate.Download(ctx, tag, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return err
		}
		if err := selfupdate.Replace(self, bin); err != nil {
			return err
		}
		ref = tag
	} else {
		fmt.Printf("building herd@%s with go install ...\n", ref)
		if err := selfupdate.GoInstall(ref, filepath.Dir(self)); err != nil {
			return err
		}
	}
	fmt.Printf("updated herd %s -> %s (%s)\n", cur, ref, self)
	// a running herd window switches to the new binary
	if _, err := exec.LookPath("tmux"); err == nil {
		if eng, _, err := loadEngine(); err == nil && eng.T.HasSession(eng.Main()) {
			if err := eng.RestartSidebar(); err == nil {
				fmt.Println("restarted the herd sidebar")
			}
		}
	}
	return nil
}
