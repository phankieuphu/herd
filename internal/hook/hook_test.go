package hook

import (
	"strings"
	"testing"
	"time"

	"github.com/phankieuphu/herd/internal/model"
)

func bg(n int) func(string) BGCount {
	return func(string) BGCount {
		c := BGCount{Total: n, Kinds: map[string]int{}}
		if n > 0 {
			c.Kinds["shell"] = n
		}
		return c
	}
}

func TestClaudeUpdate(t *testing.T) {
	cases := []struct {
		ev     ClaudeEvent
		prev   model.State
		count  int
		want   model.State
		unread bool
	}{
		{ClaudeEvent{HookEventName: "SessionStart"}, "", 0, model.Done, true},
		{ClaudeEvent{HookEventName: "UserPromptSubmit"}, model.Done, 0, model.Working, false},
		{ClaudeEvent{HookEventName: "PreToolUse", ToolName: "Bash"}, model.Working, 0, model.Working, false},
		{ClaudeEvent{HookEventName: "Notification", Message: "Claude needs your permission to use Bash"}, model.Working, 0, model.NeedsYou, true},
		{ClaudeEvent{HookEventName: "PostToolUse"}, model.NeedsYou, 0, model.Working, false},
		{ClaudeEvent{HookEventName: "Stop", TranscriptPath: "/t"}, model.Working, 0, model.Done, true},
		{ClaudeEvent{HookEventName: "Stop", TranscriptPath: "/t"}, model.Working, 2, model.Background, false},
		{ClaudeEvent{HookEventName: "SubagentStop", TranscriptPath: "/t"}, model.Working, 0, "", false},
		{ClaudeEvent{HookEventName: "SubagentStop", TranscriptPath: "/t"}, model.Background, 0, model.Done, true},
		{ClaudeEvent{HookEventName: "Notification", Message: "Claude is waiting for your input"}, model.Done, 0, "", false},
		{ClaudeEvent{HookEventName: "Notification", NotificationType: "idle_prompt"}, model.Working, 0, model.Done, true},
		{ClaudeEvent{HookEventName: "SessionEnd"}, model.Done, 0, model.Exited, false},
		{ClaudeEvent{HookEventName: "Stop", TranscriptPath: "/t"}, model.Done, 0, model.Done, false},
	}
	for i, c := range cases {
		u := ClaudeUpdate(c.ev, c.prev, bg(c.count))
		if u.State != c.want || u.MarkUnread != c.unread {
			t.Errorf("%d %s: got %q unread=%v, want %q unread=%v", i, c.ev.HookEventName, u.State, u.MarkUnread, c.want, c.unread)
		}
		if !u.Hooked {
			t.Errorf("%d: hooked not set", i)
		}
	}
}

func TestUpdateCommands(t *testing.T) {
	u := Update{State: model.Done, Hooked: true, Transcript: "/x.jsonl", MarkUnread: true, BG: &BGCount{Total: 1, Kinds: map[string]int{"shell": 1}}}
	cmds := u.Commands("%3", time.Unix(5, 0))
	var flat []string
	for _, c := range cmds {
		flat = append(flat, strings.Join(c, " "))
	}
	got := strings.Join(flat, "\n")
	for _, want := range []string{
		"set-option -p -t %3 @herd_hooked 1",
		"set-option -p -t %3 @herd_state done",
		"set-option -p -t %3 @herd_state_ts 5000",
		"set-option -p -t %3 @herd_bg 1",
		"set-option -p -t %3 @herd_bg_kinds shell:1",
		"set-option -p -t %3 @herd_read 0",
		"@herd_transcript /x.jsonl",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

const transcript = `{"type":"user","message":{"role":"user","content":"run the server and the tests"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"npm run dev","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"Command running in background with ID: bash_1"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"sleep 100","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"Command running in background with ID: bash_2"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Task","input":{"description":"review","prompt":"review code"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t4","name":"Task","input":{"description":"done one"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t4","content":"all good"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t5","name":"BashOutput","input":{"bash_id":"bash_2"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t5","content":"<status>completed</status> bash_2 exit code 0"}]}}
not json at all
{"type":"assistant","message":{"content":[{"type":"text","text":"Started the server."}]}}
`

func TestCountBackground(t *testing.T) {
	c := CountBackground(strings.NewReader(transcript))
	if c.Total != 2 || c.Kinds["shell"] != 1 || c.Kinds["subagent"] != 1 {
		t.Fatalf("got %+v", c)
	}
	if c.KindsString() != "shell:1,subagent:1" {
		t.Fatalf("kinds %q", c.KindsString())
	}
}

func TestCountBackgroundKill(t *testing.T) {
	tr := transcript + `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t6","name":"KillShell","input":{"shell_id":"bash_1"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t6","content":"ok"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":"review done"}]}}
`
	c := CountBackground(strings.NewReader(tr))
	if c.Total != 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestCountBackgroundNotification(t *testing.T) {
	tr := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"run_in_background":true}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"Command running in background with ID: b77"}]}}
{"type":"user","message":{"content":"<task-notification><task-id>b77</task-id><status>completed</status></task-notification>"}}
`
	if c := CountBackground(strings.NewReader(tr)); c.Total != 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestOnlySubagents(t *testing.T) {
	if !OnlySubagents("subagent:2") || OnlySubagents("shell:1,subagent:1") || OnlySubagents("") {
		t.Fatal("OnlySubagents wrong")
	}
}

func TestCodexUpdate(t *testing.T) {
	u := CodexUpdate(CodexEvent{Type: "agent-turn-complete"})
	if u.State != model.Done || !u.MarkUnread || u.Hooked {
		t.Fatalf("%+v", u)
	}
	if u := CodexUpdate(CodexEvent{Type: "other"}); u.State != "" {
		t.Fatalf("%+v", u)
	}
}
