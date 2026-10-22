package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type dialogKind int

const (
	dlgNone dialogKind = iota
	dlgNew
	dlgPreset
	dlgRename
	dlgRenameGroup
	dlgGroup
	dlgMessage
	dlgConfirm
	dlgHelp
	dlgFilter
)

// dialog is a small form shown at the bottom of the sidebar.
type dialog struct {
	kind   dialogKind
	title  string
	inputs []textinput.Model
	labels []string
	focus  int

	// new agent
	kinds    []string
	kindIdx  int
	worktree bool

	// lists (presets, group suggestions)
	options []string
	optIdx  int

	// confirm
	onYes func() tea.Cmd
	// target agent or group
	target string
}

func newInput(placeholder, value string, width int) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.CursorEnd()
	ti.Width = width
	ti.CharLimit = 4096
	return ti
}

func (d *dialog) focusInput() tea.Cmd {
	var cmd tea.Cmd
	for i := range d.inputs {
		if i == d.inputIndex() {
			cmd = d.inputs[i].Focus()
		} else {
			d.inputs[i].Blur()
		}
	}
	return cmd
}

// inputIndex maps the focused field to an input index (-1 when the field
// is not a text input).
func (d *dialog) inputIndex() int {
	if d.kind == dlgNew {
		// fields: 0 agent, 1 folder, 2 name, 3 prompt, 4 worktree
		if d.focus >= 1 && d.focus <= 3 {
			return d.focus - 1
		}
		return -1
	}
	if d.focus < len(d.inputs) {
		return d.focus
	}
	return -1
}

func (d *dialog) fields() int {
	if d.kind == dlgNew {
		return 5
	}
	return len(d.inputs)
}

func (d *dialog) value(i int) string { return strings.TrimSpace(d.inputs[i].Value()) }

// complete fills the group input with the first suggestion that starts with it.
func (d *dialog) complete() {
	if len(d.inputs) == 0 {
		return
	}
	v := strings.ToLower(d.inputs[0].Value())
	for _, o := range d.options {
		if strings.HasPrefix(strings.ToLower(o), v) && o != d.inputs[0].Value() {
			d.inputs[0].SetValue(o)
			d.inputs[0].CursorEnd()
			return
		}
	}
}

// fuzzy reports whether all runes of q appear in s in order (case-insensitive).
func fuzzy(s, q string) bool {
	qr := []rune(strings.ToLower(q))
	i := 0
	for _, r := range strings.ToLower(s) {
		if i < len(qr) && qr[i] == r {
			i++
		}
	}
	return i == len(qr)
}
