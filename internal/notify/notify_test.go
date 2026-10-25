package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/tmux"
)

func TestAway(t *testing.T) {
	now := time.Unix(1000, 0)
	if !Away(nil, now, time.Minute) {
		t.Fatal("no clients = away")
	}
	cs := []tmux.ClientInfo{{Activity: 990}, {Activity: 900}}
	if Away(cs, now, time.Minute) {
		t.Fatal("active 10s ago")
	}
	if !Away(cs, now, 5*time.Second) {
		t.Fatal("idle longer than 5s")
	}
}

func TestPushNtfy(t *testing.T) {
	var gotTitle, gotBody, gotPrio, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotTitle, gotBody, gotPrio, gotAuth = r.Header.Get("Title"), string(b), r.Header.Get("Priority"), r.Header.Get("Authorization")
	}))
	defer srv.Close()
	p := config.Push{Provider: "ntfy", NtfyURL: srv.URL + "/topic", NtfyToken: "tk"}
	if err := Push(context.Background(), p, Event{Title: "fix auth", Body: "needs you", State: "needs_you"}); err != nil {
		t.Fatal(err)
	}
	if gotTitle != "herd · fix auth" || gotBody != "needs you" || gotPrio != "high" || gotAuth != "Bearer tk" {
		t.Fatalf("%q %q %q %q", gotTitle, gotBody, gotPrio, gotAuth)
	}
}

func TestPushErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	ev := Event{Title: "a", Body: "b"}
	if err := Push(context.Background(), config.Push{NtfyURL: srv.URL}, ev); err == nil {
		t.Fatal("403 should fail")
	}
	if err := Push(context.Background(), config.Push{Provider: "ntfy"}, ev); err == nil {
		t.Fatal("missing url")
	}
	if err := Push(context.Background(), config.Push{Provider: "telegram"}, ev); err == nil {
		t.Fatal("missing telegram settings")
	}
	if err := Push(context.Background(), config.Push{Provider: "pigeon"}, ev); err == nil {
		t.Fatal("unknown provider")
	}
}

func TestOsaQuote(t *testing.T) {
	if got := osaQuote(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Fatal(got)
	}
}

func TestNotifierArgs(t *testing.T) {
	got := strings.Join(notifierArgs(Event{Title: "fix auth", Body: "done", Pane: "%3"}, "com.googlecode.iterm2", "herd show %3"), "|")
	want := "-title|herd · fix auth|-message|done|-group|herd-%3|-activate|com.googlecode.iterm2|-execute|herd show %3"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := strings.Join(notifierArgs(Event{Title: "x", Body: "y"}, "", ""), "|"); strings.Contains(got, "-activate") || strings.Contains(got, "-execute") {
		t.Fatalf("empty app/click: %q", got)
	}
}

func TestTerminalApp(t *testing.T) {
	t.Setenv("__CFBundleIdentifier", "com.mitchellh.ghostty")
	if TerminalApp("") != "com.mitchellh.ghostty" || TerminalApp("com.googlecode.iterm2") != "com.googlecode.iterm2" {
		t.Fatal("bundle id")
	}
	t.Setenv("__CFBundleIdentifier", "")
	if TerminalApp("") != "com.apple.Terminal" {
		t.Fatal("fallback")
	}
}
