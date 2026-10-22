package core

import (
	"strings"
)

const marker = "<^herd-pane^> "

// Capture returns the visible text of each pane, in one tmux call when possible.
func (e *Engine) Capture(ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	var cmds [][]string
	for _, id := range ids {
		cmds = append(cmds,
			[]string{"display-message", "-p", "-t", id, marker + id},
			[]string{"capture-pane", "-p", "-t", id})
	}
	text, err := e.T.Batch(cmds...)
	if err != nil {
		// a pane went away between listing and capturing; do them one by one
		for _, id := range ids {
			if t, err := e.T.Run("capture-pane", "-p", "-t", id); err == nil {
				out[id] = t
			}
		}
		return out
	}
	parts := strings.Split(text, marker)
	for _, part := range parts[1:] {
		id, body, _ := strings.Cut(part, "\n")
		out[strings.TrimSpace(id)] = body
	}
	return out
}
