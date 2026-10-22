package install

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeClaude(t *testing.T) {
	in := `{
  "model": "opus",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "~/.claude/hooks/mine.sh"}]}]
  }
}`
	out, changed, err := MergeClaude([]byte(in), "/usr/local/bin/herd")
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	if root["model"] != "opus" {
		t.Fatal("lost other settings")
	}
	hooks := root["hooks"].(map[string]any)
	for _, ev := range ClaudeEvents {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("missing %s", ev)
		}
	}
	stop := hooks["Stop"].([]any)
	if len(stop) != 2 || !strings.Contains(string(out), "mine.sh") {
		t.Fatalf("existing Stop hook not kept: %s", out)
	}
	pre := hooks["PreToolUse"].([]any)[0].(map[string]any)
	if pre["matcher"] != "*" {
		t.Fatal("tool events need a matcher")
	}
	if strings.Contains(string(out), `&`) {
		t.Fatal("html escaping")
	}

	// second run changes nothing
	out2, changed, err := MergeClaude(out, "/usr/local/bin/herd")
	if err != nil || changed || string(out2) != string(out) {
		t.Fatalf("not idempotent: changed=%v", changed)
	}
	// moved binary updates the path, no duplicates
	out3, changed, _ := MergeClaude(out, "/opt/herd/herd")
	if !changed || strings.Contains(string(out3), "/usr/local/bin/herd") || strings.Count(string(out3), "hook claude") != len(ClaudeEvents) {
		t.Fatalf("path update: %s", out3)
	}

	// uninstall keeps the user's hook
	back, changed, err := RemoveClaude(out3)
	if err != nil || !changed || strings.Contains(string(back), "hook claude") || !strings.Contains(string(back), "mine.sh") {
		t.Fatalf("remove: %s", back)
	}
}

func TestMergeClaudeEmptyAndBad(t *testing.T) {
	if _, changed, err := MergeClaude(nil, "/h"); err != nil || !changed {
		t.Fatal(err)
	}
	if _, _, err := MergeClaude([]byte("{nope"), "/h"); err == nil {
		t.Fatal("want error")
	}
}

func TestMergeCodex(t *testing.T) {
	out, changed, err := MergeCodex([]byte("model = \"o4\"\n\n[tui]\nx = 1\n"), "/bin/herd")
	if err != nil || !changed || !strings.HasPrefix(string(out), `notify = ["/bin/herd", "hook", "codex"]`) {
		t.Fatalf("%v %s", err, out)
	}
	if _, changed, _ := MergeCodex(out, "/bin/herd"); changed {
		t.Fatal("not idempotent")
	}
	if out2, changed, _ := MergeCodex(out, "/new/herd"); !changed || !strings.Contains(string(out2), "/new/herd") {
		t.Fatal("path update")
	}
	if _, _, err := MergeCodex([]byte(`notify = ["terminal-notifier"]`), "/bin/herd"); err == nil {
		t.Fatal("must not overwrite another notify program")
	}
}
