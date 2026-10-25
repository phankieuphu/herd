// Package notify sends "an agent needs you" messages: desktop, tmux and phone push.
package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/tmux"
)

// Event is one notification.
type Event struct {
	Title string // agent name
	Body  string // "needs you", "done"
	State string // needs_you | done | exited
	Pane  string // agent pane, for the click action
}

// Desktop shows a desktop notification when a notifier is available.
//
// On macOS it uses terminal-notifier when installed: clicking the
// notification brings app (a bundle id, see TerminalApp) to the front and
// runs the shell command onClick. Without it, osascript is used; macOS
// shows those notifications as Script Editor's, so a click opens Script
// Editor.
func Desktop(ev Event, app, onClick string) error {
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("terminal-notifier"); err == nil {
			return exec.Command(p, notifierArgs(ev, app, onClick)...).Run()
		}
		script := fmt.Sprintf("display notification %s with title %s", osaQuote(ev.Body), osaQuote("herd · "+ev.Title))
		return exec.Command("osascript", "-e", script).Run()
	default:
		if p, err := exec.LookPath("notify-send"); err == nil {
			return exec.Command(p, "-a", "herd", "herd · "+ev.Title, ev.Body).Run()
		}
	}
	return nil
}

func notifierArgs(ev Event, app, onClick string) []string {
	args := []string{"-title", "herd · " + ev.Title, "-message", ev.Body, "-group", "herd-" + ev.Pane}
	if app != "" {
		args = append(args, "-activate", app)
	}
	if onClick != "" {
		args = append(args, "-execute", onClick)
	}
	return args
}

// TerminalApp is the bundle id of the terminal to bring to the front when a
// notification is clicked: the setting if given, else the app herd was
// started from (macOS passes it down as __CFBundleIdentifier), else
// Terminal.app.
func TerminalApp(setting string) string {
	if setting != "" {
		return setting
	}
	if id := os.Getenv("__CFBundleIdentifier"); id != "" {
		return id
	}
	return "com.apple.Terminal"
}

// HasClickAction reports whether clicking a desktop notification can open
// the agent (on macOS this needs terminal-notifier).
func HasClickAction() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	_, err := exec.LookPath("terminal-notifier")
	return err == nil
}

func osaQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Tmux flashes a message in the status line of every attached client.
func Tmux(t *tmux.Client, clients []tmux.ClientInfo, ev Event) {
	msg := fmt.Sprintf("herd: %s %s", ev.Title, ev.Body)
	var cmds [][]string
	for _, c := range clients {
		cmds = append(cmds, []string{"display-message", "-c", c.Name, "-d", "4000", msg})
	}
	_, _ = t.Batch(cmds...)
}

// Push sends a phone notification through ntfy or Telegram.
func Push(ctx context.Context, p config.Push, ev Event) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	switch p.Provider {
	case "", "ntfy":
		if p.NtfyURL == "" {
			return fmt.Errorf("notify.push.ntfy_url is empty")
		}
		req, err := http.NewRequestWithContext(ctx, "POST", p.NtfyURL, strings.NewReader(ev.Body))
		if err != nil {
			return err
		}
		req.Header.Set("Title", "herd · "+ev.Title)
		if ev.State == "needs_you" {
			req.Header.Set("Priority", "high")
			req.Header.Set("Tags", "warning")
		} else {
			req.Header.Set("Tags", "white_check_mark")
		}
		if p.NtfyToken != "" {
			req.Header.Set("Authorization", "Bearer "+p.NtfyToken)
		}
		return do(req)
	case "telegram":
		if p.TelegramToken == "" || p.TelegramChatID == "" {
			return fmt.Errorf("notify.push.telegram_token and telegram_chat_id are required")
		}
		form := url.Values{"chat_id": {p.TelegramChatID}, "text": {"herd · " + ev.Title + ": " + ev.Body}}
		req, err := http.NewRequestWithContext(ctx, "POST",
			"https://api.telegram.org/bot"+p.TelegramToken+"/sendMessage", bytes.NewBufferString(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return do(req)
	}
	return fmt.Errorf("unknown push provider %q", p.Provider)
}

func do(req *http.Request) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("push: %s", resp.Status)
	}
	return nil
}

// Away reports whether the user has not touched any tmux client for d.
func Away(clients []tmux.ClientInfo, now time.Time, d time.Duration) bool {
	if len(clients) == 0 {
		return true
	}
	var last int64
	for _, c := range clients {
		if c.Activity > last {
			last = c.Activity
		}
	}
	return now.Sub(time.Unix(last, 0)) >= d
}
