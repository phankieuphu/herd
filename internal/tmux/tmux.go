// Package tmux is a small wrapper around the tmux command line client.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Sep separates fields in format output. tmux escapes control characters
// in format output, so this must be printable (and must not contain '#').
const Sep = "<^>"

// Client runs tmux commands. Socket selects a server with -L (empty = default
// server, or the one from $TMUX when running inside tmux).
type Client struct {
	Bin    string
	Socket string
}

// New returns a client. HERD_TMUX_SOCKET picks a named server (used by tests).
func New() *Client {
	return &Client{Bin: "tmux", Socket: os.Getenv("HERD_TMUX_SOCKET")}
}

// argv adds -u (keep UTF-8 in titles and paths even with a C locale) and
// the socket name.
func (c *Client) argv(a []string) []string {
	pre := []string{"-u"}
	if c.Socket != "" {
		pre = append(pre, "-L", c.Socket)
	}
	return append(pre, a...)
}

// Command builds an exec.Cmd for interactive use (attach, switch-client).
func (c *Client) Command(a ...string) *exec.Cmd {
	cmd := exec.Command(c.Bin, c.argv(a)...)
	if c.Socket != "" {
		// make sure a nested client talks to the right server
		cmd.Env = append(os.Environ(), "HERD_TMUX_SOCKET="+c.Socket)
	}
	return cmd
}

// Run runs one tmux command and returns stdout without the trailing newline.
func (c *Client) Run(a ...string) (string, error) {
	cmd := exec.Command(c.Bin, c.argv(a)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("tmux %s: %s", a[0], msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Batch runs several commands in one tmux call, separated by ";".
func (c *Client) Batch(cmds ...[]string) (string, error) {
	var args []string
	for i, cmd := range cmds {
		if len(cmd) == 0 {
			continue
		}
		if i > 0 && len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, cmd...)
	}
	if len(args) == 0 {
		return "", nil
	}
	return c.Run(args...)
}

// ServerRunning reports whether a tmux server answers.
func (c *Client) ServerRunning() bool {
	_, err := c.Run("list-sessions", "-F", "#{session_name}")
	return err == nil
}

// HasSession reports whether a session with this exact name exists.
func (c *Client) HasSession(name string) bool {
	_, err := c.Run("has-session", "-t", "="+name)
	return err == nil
}

// Version returns tmux major/minor version, e.g. 3.4 -> (3, 4).
func (c *Client) Version() (int, int, error) {
	out, err := exec.Command(c.Bin, "-V").Output()
	if err != nil {
		return 0, 0, err
	}
	return ParseVersion(string(out))
}

// ParseVersion parses `tmux -V` output like "tmux 3.4" or "tmux next-3.5a".
func ParseVersion(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, "0123456789")
	if i < 0 {
		return 0, 0, errors.New("no version in " + strconv.Quote(s))
	}
	s = s[i:]
	major, rest, _ := strings.Cut(s, ".")
	ma, err := strconv.Atoi(major)
	if err != nil {
		return 0, 0, err
	}
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	mi, _ := strconv.Atoi(rest[:j])
	return ma, mi, nil
}

// SetPaneOpt returns the command to set a pane user option.
func SetPaneOpt(pane, key, val string) []string {
	return []string{"set-option", "-p", "-t", pane, key, val}
}

// UnsetPaneOpt returns the command to unset a pane user option.
func UnsetPaneOpt(pane, key string) []string {
	return []string{"set-option", "-p", "-u", "-t", pane, key}
}

// Format joins format variables with Sep.
func Format(vars ...string) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
		parts[i] = "#{" + v + "}"
	}
	return strings.Join(parts, Sep)
}

// Lines splits output into non-empty lines.
func Lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Client describes one attached tmux client.
type ClientInfo struct {
	Name     string
	Session  string
	Activity int64 // unix seconds
	Pane     string
}

// ListClients lists attached clients.
func (c *Client) ListClients() ([]ClientInfo, error) {
	out, err := c.Run("list-clients", "-F", Format("client_name", "client_session", "client_activity", "pane_id"))
	if err != nil {
		if strings.Contains(err.Error(), "no server") || strings.Contains(err.Error(), "No such file") {
			return nil, nil
		}
		return nil, err
	}
	var res []ClientInfo
	for _, l := range Lines(out) {
		f := strings.Split(l, Sep)
		if len(f) < 4 {
			continue
		}
		act, _ := strconv.ParseInt(f[2], 10, 64)
		res = append(res, ClientInfo{Name: f[0], Session: f[1], Activity: act, Pane: f[3]})
	}
	return res, nil
}

// Quote quotes a string for a POSIX shell.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
