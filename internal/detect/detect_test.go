package detect

import (
	"testing"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/model"
)

func rules(kind string) Rules { return Compile(config.BuiltinAgents()[kind]) }

func TestScrapeClaude(t *testing.T) {
	r := rules("claude")
	cases := []struct {
		name   string
		screen string
		want   model.State
	}{
		{"empty", "\n\n   \n", model.Unknown},
		{"working", "> fix the bug\n\n✻ Thinking… (12s · ↑ 1.2k tokens · esc to interrupt)\n\n╭──╮\n│ > │\n╰──╯\n", model.Working},
		{"permission", "Bash command\n  rm -rf build\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again\n  3. No\n", model.NeedsYou},
		{"edit", "Do you want to make this edit to main.go?\n❯ 1. Yes\n", model.NeedsYou},
		{"idle", "⏺ Done. I fixed the bug in parser.go.\n\n╭────────╮\n│ >      │\n╰────────╯\n  ? for shortcuts\n", model.Done},
	}
	for _, c := range cases {
		if got := Scrape(r, c.screen); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestScrapeCodex(t *testing.T) {
	r := rules("codex")
	if got := Scrape(r, "• Working (14s • esc to interrupt)\n▌ \n"); got != model.Working {
		t.Errorf("working: %s", got)
	}
	if got := Scrape(r, "Would you like to run the following command?\n$ go test ./...\n▌ Yes, proceed\n"); got != model.NeedsYou {
		t.Errorf("approval: %s", got)
	}
	if got := Scrape(r, "codex\nAll tests pass.\n\n▌ Ask Codex to do anything\n"); got != model.Done {
		t.Errorf("idle: %s", got)
	}
}

func TestScrapeOnlyLooksAtBottom(t *testing.T) {
	r := rules("claude")
	screen := "Do you want to proceed?\n"
	for i := 0; i < 40; i++ {
		screen += "line\n"
	}
	if got := Scrape(r, screen); got != model.Done {
		t.Errorf("old prompt far above should not count, got %s", got)
	}
}

func TestBadPatternsSkipped(t *testing.T) {
	a := config.Agent{NeedsYou: []string{"(unclosed", "ok"}, Working: []string{"[z-a]"}}
	r := Compile(a)
	if len(r.NeedsYou) != 1 || len(r.Working) != 0 {
		t.Fatalf("got %d/%d", len(r.NeedsYou), len(r.Working))
	}
	if bad := BadPatterns(a); len(bad) != 2 {
		t.Fatalf("bad = %v", bad)
	}
}

func TestResolve(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	old := now.Add(-time.Hour)
	cases := []struct {
		name string
		in   Input
		want model.State
	}{
		{"shell idle", Input{Kind: "shell", Cmd: "zsh"}, model.Done},
		{"shell running", Input{Kind: "shell", Cmd: "make"}, model.Working},
		{"agent exited", Input{Kind: "claude", Cmd: "bash", Created: old}, model.Exited},
		{"wrapper at prompt", Input{Kind: "claude", Cmd: "bash", Created: old, Scraped: model.NeedsYou}, model.NeedsYou},
		{"agent starting", Input{Kind: "claude", Cmd: "bash", Created: now.Add(-time.Second)}, model.Working},
		{"no hooks uses screen", Input{Kind: "codex", Cmd: "codex", Created: old, Scraped: model.Working}, model.Working},
		{"hook working", Input{Kind: "claude", Cmd: "claude", Created: old, Hooked: true, HookState: model.Working, Scraped: model.Done}, model.Working},
		{"screen prompt beats hook", Input{Kind: "claude", Cmd: "claude", Created: old, Hooked: true, HookState: model.Working, Scraped: model.NeedsYou}, model.NeedsYou},
		{"answered prompt", Input{Kind: "claude", Cmd: "claude", Created: old, Hooked: true, HookState: model.NeedsYou, Scraped: model.Working}, model.Working},
		{"done with bg", Input{Kind: "claude", Cmd: "claude", Created: old, Hooked: true, HookState: model.Done, BG: 2, Scraped: model.Done}, model.Background},
		{"bg finished", Input{Kind: "claude", Cmd: "claude", Created: old, Hooked: true, HookState: model.Background, BG: 0, Scraped: model.Done}, model.Done},
		{"session end", Input{Kind: "claude", Cmd: "claude", Created: old, Hooked: true, HookState: model.Exited}, model.Exited},
		{"unknown", Input{Kind: "codex", Cmd: "codex", Created: old}, model.Unknown},
	}
	for _, c := range cases {
		if got := Resolve(c.in, now); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestDebouncer(t *testing.T) {
	d := &Debouncer{Grace: 2 * time.Second}
	t0 := time.Unix(100, 0)
	if s := d.Feed(model.Working, t0); s != model.Working {
		t.Fatal(s)
	}
	// a blink of "done" is ignored
	if s := d.Feed(model.Done, t0.Add(time.Second)); s != model.Working {
		t.Fatalf("blink: %s", s)
	}
	if s := d.Feed(model.Working, t0.Add(1500*time.Millisecond)); s != model.Working {
		t.Fatal(s)
	}
	// steady done becomes done after the grace
	d.Feed(model.Done, t0.Add(3*time.Second))
	if s := d.Feed(model.Done, t0.Add(4*time.Second)); s != model.Working {
		t.Fatalf("too early: %s", s)
	}
	if s := d.Feed(model.Done, t0.Add(5*time.Second)); s != model.Done {
		t.Fatalf("after grace: %s", s)
	}
	// needs you shows at once
	if s := d.Feed(model.NeedsYou, t0.Add(5100*time.Millisecond)); s != model.NeedsYou {
		t.Fatalf("needs you: %s", s)
	}
}
