package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/tmux"
)

// runPlaceholder draws a calm message and ignores keys. It keeps a slot:
// the stage when no agent is shown, or an agent's own session while the
// agent is shown in the herd window.
func runPlaceholder(cfg *config.Config) {
	fd := os.Stdin.Fd()
	if st, err := term.MakeRaw(fd); err == nil {
		defer term.Restore(fd, st)
	}
	signal.Ignore(syscall.SIGINT, syscall.SIGTSTP, syscall.SIGQUIT)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGHUP, syscall.SIGTERM)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	// swallow typed keys so they do not echo
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				return
			}
		}
	}()

	t := tmux.New()
	pane := os.Getenv("TMUX_PANE")
	main := cfg.Sidebar.Session
	last := ""
	draw := func(force bool) {
		session := ""
		if pane != "" {
			session, _ = t.Run("display-message", "-p", "-t", pane, "#{session_name}")
		}
		if session == last && !force {
			return
		}
		last = session
		var lines []string
		if session == "" || session == main {
			lines = []string{
				"\x1b[1mherd\x1b[0m", "",
				"No agent on screen.", "",
				"\x1b[2mPress n in the sidebar to start one,",
				"or ⏎ on an agent to show it here.\x1b[0m",
			}
		} else {
			name := strings.TrimPrefix(session, "herd-")
			lines = []string{
				"\x1b[1m" + name + "\x1b[0m is open in the herd window.", "",
				"\x1b[2mTo bring it here, run:\x1b[0m",
				"herd attach " + name,
			}
		}
		center(lines)
	}
	draw(true)
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			fmt.Print("\x1b[?25h")
			return
		case <-winch:
			draw(true)
		case <-tk.C:
			draw(false)
		}
	}
}

func visibleLen(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc:
			if r == 'm' {
				esc = false
			}
		default:
			n++
		}
	}
	return n
}

func center(lines []string) {
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 {
		w, h = 80, 24
	}
	var b strings.Builder
	b.WriteString("\x1b[?25l\x1b[2J\x1b[H")
	top := (h - len(lines)) / 2
	if top < 0 {
		top = 0
	}
	for i, l := range lines {
		pad := (w - visibleLen(l)) / 2
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s", top+i+1, pad+1, l)
	}
	os.Stdout.WriteString(b.String())
}
