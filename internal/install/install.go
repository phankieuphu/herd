// Package install adds herd's hooks to Claude Code and Codex settings.
package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phankieuphu/herd/internal/tmux"
)

// ClaudeEvents are the hook events herd listens to.
var ClaudeEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Notification", "Stop", "SubagentStop", "PreCompact", "SessionEnd",
}

var toolEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true}

// HookCommand is the command Claude Code runs.
func HookCommand(self string) string { return tmux.Quote(self) + " hook claude" }

func isHerdHook(cmd string) bool {
	return strings.Contains(cmd, "hook claude") && strings.Contains(cmd, "herd")
}

// MergeClaude adds herd hooks to a Claude Code settings.json document. Other
// settings and hooks stay as they are. It returns the new document and
// whether anything changed.
func MergeClaude(data []byte, self string) ([]byte, bool, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, false, fmt.Errorf("settings.json is not valid JSON: %w", err)
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	changed := false
	cmd := HookCommand(self)
	for _, ev := range ClaudeEvents {
		groups, _ := hooks[ev].([]any)
		found := false
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			list, _ := gm["hooks"].([]any)
			for _, h := range list {
				hm, _ := h.(map[string]any)
				if c, _ := hm["command"].(string); isHerdHook(c) {
					found = true
					if c != cmd { // binary moved: update the path
						hm["command"] = cmd
						changed = true
					}
				}
			}
		}
		if found {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 5}},
		}
		if toolEvents[ev] {
			entry["matcher"] = "*"
		}
		hooks[ev] = append(groups, entry)
		changed = true
	}
	root["hooks"] = hooks
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), changed, nil
}

// RemoveClaude takes herd hooks out of settings.json.
func RemoveClaude(data []byte) ([]byte, bool, error) {
	root := map[string]any{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	changed := false
	for ev, v := range hooks {
		groups, _ := v.([]any)
		var keepGroups []any
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			list, _ := gm["hooks"].([]any)
			var keep []any
			for _, h := range list {
				hm, _ := h.(map[string]any)
				if c, _ := hm["command"].(string); isHerdHook(c) {
					changed = true
					continue
				}
				keep = append(keep, h)
			}
			if len(keep) > 0 {
				gm["hooks"] = keep
				keepGroups = append(keepGroups, gm)
			}
		}
		if len(keepGroups) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = keepGroups
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), changed, nil
}

var notifyRe = regexp.MustCompile(`(?m)^\s*notify\s*=.*$`)

// MergeCodex sets `notify` in a Codex config.toml. If notify is already set
// to something else, it does not touch the file and returns an error that
// explains what to add.
func MergeCodex(data []byte, self string) ([]byte, bool, error) {
	line := fmt.Sprintf("notify = [%q, \"hook\", \"codex\"]", self)
	s := string(data)
	if m := notifyRe.FindString(s); m != "" {
		if strings.Contains(m, "hook") && strings.Contains(m, "codex") && strings.Contains(m, "herd") {
			if strings.TrimSpace(m) == line {
				return data, false, nil
			}
			return []byte(strings.Replace(s, m, line, 1)), true, nil
		}
		return nil, false, fmt.Errorf("codex config already has %q; herd needs %s (Codex runs only one notify program)", strings.TrimSpace(m), line)
	}
	// top-level keys must come before the first [table]
	return []byte(line + "\n" + s), true, nil
}

// WriteWithBackup writes data to path and keeps the old file as path.herd-bak.
func WriteWithBackup(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".herd-bak", old, 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".herd-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClaudeSettingsPath is ~/.claude/settings.json (or $CLAUDE_CONFIG_DIR).
func ClaudeSettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// CodexConfigPath is ~/.codex/config.toml (or $CODEX_HOME).
func CodexConfigPath() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

// ClaudeInstalled reports whether settings.json has herd hooks.
func ClaudeInstalled() bool {
	data, err := os.ReadFile(ClaudeSettingsPath())
	return err == nil && bytes.Contains(data, []byte("hook claude"))
}

// CodexInstalled reports whether config.toml calls herd.
func CodexInstalled() bool {
	data, err := os.ReadFile(CodexConfigPath())
	return err == nil && bytes.Contains(data, []byte("hook\", \"codex"))
}
