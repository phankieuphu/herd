package hook

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BGCount is the number of background tasks still running in a session.
type BGCount struct {
	Total int
	Kinds map[string]int // "subagent", "shell"
}

// KindsString renders kinds as "shell:1,subagent:2".
func (b BGCount) KindsString() string {
	var keys []string
	for k := range b.Kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+":"+strconv.Itoa(b.Kinds[k]))
	}
	return strings.Join(parts, ",")
}

// OnlySubagents reports whether every running task is a subagent.
func OnlySubagents(kinds string) bool {
	if kinds == "" {
		return false
	}
	for _, p := range strings.Split(kinds, ",") {
		if !strings.HasPrefix(p, "subagent:") {
			return false
		}
	}
	return true
}

type contentItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

var (
	bgIDRe   = regexp.MustCompile(`(?i)(?:background|async)[^\n]{0,120}?\b(?:ID|id|agentId|shell_id|task_id)\b["':= ]+\s*([A-Za-z0-9_\-]{3,})`)
	finishRe = regexp.MustCompile(`(?i)(completed|failed|killed|exited|exit code|terminated|finished|stopped)`)
)

// textOf flattens a tool_result content (string or list of text blocks).
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []contentItem
	if json.Unmarshal(raw, &items) == nil {
		var b strings.Builder
		for _, it := range items {
			if it.Text != "" {
				b.WriteString(it.Text)
				b.WriteByte('\n')
			}
		}
		return b.String()
	}
	return ""
}

func isSubagentTool(name string) bool { return name == "Task" || name == "Agent" }

// CountBackground reads a Claude Code transcript (JSON lines) and counts
// work still running after the turn: subagent calls without a result, and
// background shells/agents that were started but never reported finished.
func CountBackground(r io.Reader) BGCount {
	type tool struct {
		name string
		bg   bool
	}
	tools := map[string]tool{}     // tool_use id -> tool
	resulted := map[string]bool{}  // tool_use ids with a result
	running := map[string]string{} // background id -> kind
	var order []string             // background ids in start order

	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var tl transcriptLine
			if json.Unmarshal(line, &tl) == nil && len(tl.Message.Content) > 0 && tl.Message.Content[0] == '[' {
				var items []contentItem
				if json.Unmarshal(tl.Message.Content, &items) == nil {
					for _, it := range items {
						switch it.Type {
						case "tool_use":
							var in struct {
								RunInBackground bool   `json:"run_in_background"`
								ShellID         string `json:"shell_id"`
								BashID          string `json:"bash_id"`
								TaskID          string `json:"task_id"`
							}
							_ = json.Unmarshal(it.Input, &in)
							tools[it.ID] = tool{name: it.Name, bg: in.RunInBackground}
							switch it.Name {
							case "KillShell", "KillBash", "TaskStop", "KillTask":
								for _, id := range []string{in.ShellID, in.BashID, in.TaskID} {
									if id != "" {
										delete(running, id)
									}
								}
							}
						case "tool_result":
							resulted[it.ToolUseID] = true
							txt := textOf(it.Content)
							t := tools[it.ToolUseID]
							if t.bg {
								if m := bgIDRe.FindStringSubmatch(txt); m != nil {
									kind := "shell"
									if isSubagentTool(t.name) {
										kind = "subagent"
									}
									if _, seen := running[m[1]]; !seen {
										order = append(order, m[1])
									}
									running[m[1]] = kind
									continue
								}
							}
							markFinished(running, txt)
						case "text":
							markFinished(running, it.Text)
						}
					}
				}
			} else if json.Unmarshal(line, &tl) == nil && len(tl.Message.Content) > 0 && tl.Message.Content[0] == '"' {
				// user messages such as <task-notification> come as plain strings
				markFinished(running, textOf(tl.Message.Content))
			}
		}
		if err != nil {
			break
		}
	}
	res := BGCount{Kinds: map[string]int{}}
	for id, t := range tools {
		if isSubagentTool(t.name) && !resulted[id] {
			res.Total++
			res.Kinds["subagent"]++
		}
	}
	for _, id := range order {
		if kind, ok := running[id]; ok {
			res.Total++
			res.Kinds[kind]++
		}
	}
	return res
}

func markFinished(running map[string]string, txt string) {
	if len(running) == 0 || txt == "" || !finishRe.MatchString(txt) {
		return
	}
	for id := range running {
		if strings.Contains(txt, id) {
			delete(running, id)
		}
	}
}

// CountBackgroundFile is CountBackground on a file path. Errors count as zero.
func CountBackgroundFile(path string) BGCount {
	f, err := os.Open(path)
	if err != nil {
		return BGCount{Kinds: map[string]int{}}
	}
	defer f.Close()
	return CountBackground(f)
}
