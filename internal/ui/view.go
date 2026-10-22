package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/phankieuphu/herd/internal/core"
	"github.com/phankieuphu/herd/internal/hook"
	"github.com/phankieuphu/herd/internal/limits"
	"github.com/phankieuphu/herd/internal/model"
)

const topLines = 1

// fit truncates or pads s (which may contain ANSI) to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// leftRight puts left and right on one line of width w.
func leftRight(left, right string, w int) string {
	rw := lipgloss.Width(right)
	if rw == 0 {
		return fit(left, w)
	}
	if rw >= w {
		return fit(right, w)
	}
	return fit(left, w-rw-1) + " " + right
}

func wrap(s string, w int) []string {
	if w <= 4 {
		return []string{s}
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line == "" {
				line = word
			} else if lipgloss.Width(line)+1+lipgloss.Width(word) <= w {
				line += " " + word
			} else {
				lines = append(lines, line)
				line = word
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func age(ts int64, now time.Time) string {
	if ts <= 0 {
		return ""
	}
	d := now.Sub(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (m Model) listHeight() int {
	h := m.height - topLines - len(m.bottomLines())
	if h < 1 {
		return 1
	}
	return h
}

// View renders the sidebar.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.dlg != nil && m.dlg.kind == dlgHelp {
		return m.helpView()
	}
	w := m.width
	var out []string
	out = append(out, m.topLine())

	compact := m.cfg.Get().Sidebar.Compact
	h := m.listHeight()
	var list []string
	if m.snap == nil {
		list = append(list, sDim.Render(" loading…"))
	} else if len(m.rows) == 0 {
		if m.dlg != nil && m.dlg.kind == dlgFilter {
			list = append(list, sDim.Render(" no match"))
		} else {
			list = append(list, "", sDim.Render(" No agents yet."), "", " "+sKey.Render("n")+sDim.Render(" start an agent"), " "+sKey.Render("?")+sDim.Render(" all keys"))
		}
	} else {
		now := time.Now()
		for i := m.offset; i < len(m.rows); i++ {
			r := m.rows[i]
			if len(list)+r.height(compact) > h {
				break
			}
			list = append(list, m.renderRow(r, i == m.cursor, compact, now)...)
		}
	}
	for len(list) < h {
		list = append(list, "")
	}
	for _, l := range list[:h] {
		out = append(out, fit(l, w))
	}
	for _, l := range m.bottomLines() {
		out = append(out, fit(l, w))
	}
	return strings.Join(out, "\n")
}

func (m Model) topLine() string {
	left := " " + sTitle.Render("herd")
	if m.cfgErr != "" {
		left += " " + sErr.Render("settings error")
	}
	var parts []string
	if m.snap != nil {
		c := core.Count(m.snap.Agents())
		if c.NeedsYou > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(cNeed).Bold(true).Render(fmt.Sprintf("◆%d", c.NeedsYou)))
		}
		if c.Working > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(cWork).Render(fmt.Sprintf("%s%d", spinner[m.frame%len(spinner)], c.Working)))
		}
		if c.Unread > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(cDone).Render(fmt.Sprintf("✓%d", c.Unread)))
		}
	}
	return leftRight(left, strings.Join(parts, " ")+" ", m.width)
}

func (m Model) renderRow(r row, selected, compact bool, now time.Time) []string {
	w := m.width
	bar := " "
	if selected {
		bar = sAccent.Render("▌")
	}
	if r.header {
		arrow := "▾"
		if r.folded {
			arrow = "▸"
		}
		txt := sHeader.Render(arrow+" "+r.group) + sFaint.Render(fmt.Sprintf(" %d", r.count))
		return []string{bar + txt}
	}
	a := r.agent
	st := a.View
	if s, ok := m.shown[a.ID]; ok {
		st = s
	}
	indent := ""
	if a.Group != "" && !(m.dlg != nil && m.dlg.kind == dlgFilter) {
		indent = " "
	}
	titleStyle := sText
	switch {
	case a.ID == m.stage:
		titleStyle = sStage
	case st == model.NeedsYou || !a.Read:
		titleStyle = sBold
	case a.Inactive || st == model.Exited:
		titleStyle = sDim
	}
	left := bar + indent + icon(st, a.Inactive, m.frame) + " " + titleStyle.Render(a.Name())
	if !a.Read && st.Attention() && !a.Inactive {
		left += " " + sUnread.Render("•")
	}
	right := ""
	switch {
	case st == model.Background && a.BG > 0:
		what := "bg"
		if hook.OnlySubagents(a.BGKinds) {
			what = "agents"
		}
		right = sDim.Render(fmt.Sprintf("%s %d", what, a.BG))
	case st.Attention() && !a.Inactive:
		right = sDim.Render(age(a.ViewTS, now))
	}

	// detail: kind · branch +a -d
	var detail []string
	detail = append(detail, a.Kind)
	branch := a.Branch
	if ds, ok := m.diffs[a.ID]; ok && ds.OK {
		if ds.Branch != "" {
			branch = ds.Branch
		}
	}
	if branch != "" {
		detail = append(detail, branch)
	}
	d := sDim.Render(strings.Join(detail, " · "))
	if ds, ok := m.diffs[a.ID]; ok && ds.OK && (ds.Added > 0 || ds.Deleted > 0) {
		d += " " + sAdd.Render(fmt.Sprintf("+%d", ds.Added)) + sDim.Render(" ") + sDel.Render(fmt.Sprintf("−%d", ds.Deleted))
	}
	if compact {
		if branch != "" {
			left += " " + sFaint.Render(branch)
		}
		return []string{leftRight(left, right, w)}
	}
	return []string{leftRight(left, right, w), " " + indent + "  " + d}
}

// bottomLines renders the dialog or preview, the message and the footer.
func (m Model) bottomLines() []string {
	var out []string
	w := m.width
	if m.dlg != nil && m.dlg.kind != dlgHelp {
		out = append(out, m.dialogLines()...)
	} else if pv := m.previewLines(); len(pv) > 0 {
		out = append(out, pv...)
	}
	if m.msg != "" && time.Now().Before(m.msgUntil) {
		st := sOK
		if m.msgErr {
			st = sErr
		}
		for _, l := range wrap(m.msg, w-2) {
			out = append(out, " "+st.Render(l))
		}
	}
	out = append(out, m.limitLines()...)
	if m.dlg == nil {
		out = append(out, " "+sKey.Render("n")+sDim.Render(" new ")+sKey.Render("⏎")+sDim.Render(" open ")+
			sKey.Render("tab")+sDim.Render(" next ")+sKey.Render("?")+sDim.Render(" keys"))
	}
	// never let the bottom eat the whole screen
	if max := m.height - topLines - 2; max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

func (m Model) separator(label string) string {
	w := m.width
	l := "── " + label + " "
	if lipgloss.Width(l) >= w {
		return sFaint.Render(fit(l, w))
	}
	return sFaint.Render(l + strings.Repeat("─", w-lipgloss.Width(l)))
}

func (m Model) previewLines() []string {
	n := m.cfg.Get().Sidebar.PreviewLines
	if !m.preview || n <= 0 || m.height < 18 {
		return nil
	}
	sel := m.selectedAgent()
	if sel == nil || sel.ID == m.stage {
		return nil
	}
	screen, ok := m.screens[sel.ID]
	if !ok {
		return nil
	}
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	// drop trailing blank lines, keep the last n
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := []string{m.separator(sel.Name())}
	for _, l := range lines {
		out = append(out, sDim.Render(fit(" "+strings.TrimRight(l, " "), m.width)))
	}
	return out
}

func (m Model) dialogLines() []string {
	d := m.dlg
	w := m.width
	out := []string{m.separator(strings.ToLower(dialogName(d.kind)))}
	switch d.kind {
	case dlgNew:
		for f := 0; f < 5; f++ {
			label := d.labels[f]
			mark, ls := "  ", sDim
			if d.focus == f {
				mark, ls = sAccent.Render("› "), sAccent
			}
			var val string
			switch f {
			case 0:
				val = sDim.Render("‹ ") + sBold.Render(d.kinds[d.kindIdx]) + sDim.Render(" ›")
			case 4:
				if d.worktree {
					val = sOK.Render("[x]") + sDim.Render(" own branch")
				} else {
					val = sDim.Render("[ ] same folder")
				}
			default:
				d.inputs[f-1].Width = w - 12
				val = d.inputs[f-1].View()
			}
			out = append(out, mark+ls.Render(fmt.Sprintf("%-8s", label))+" "+val)
		}
		if d.target != "" {
			out = append(out, sDim.Render("  group: "+d.target))
		}
		out = append(out, sDim.Render("  tab next · ←→ change · ⏎ start · esc"))
	case dlgPreset:
		out = append(out, " "+sBold.Render(d.title))
		for i, o := range d.options {
			mark := "  "
			if i == d.optIdx {
				mark = sAccent.Render("› ")
			}
			p := m.cfg.Get().Presets[o]
			out = append(out, leftRight(mark+o, sDim.Render(p.Agent)+" ", w))
		}
		out = append(out, sDim.Render("  ⏎ start · esc"))
	case dlgConfirm:
		for _, l := range wrap(d.title, w-2) {
			out = append(out, " "+sBold.Render(l))
		}
		out = append(out, " "+sKey.Render("y")+sDim.Render(" yes  ")+sKey.Render("n")+sDim.Render(" no"))
	case dlgFilter:
		d.inputs[0].Width = w - 4
		out = []string{" " + sAccent.Render("/ ") + d.inputs[0].View()}
	default:
		for _, l := range wrap(d.title, w-2) {
			out = append(out, " "+sBold.Render(l))
		}
		d.inputs[0].Width = w - 4
		out = append(out, " "+sAccent.Render("› ")+d.inputs[0].View())
		if d.kind == dlgGroup && len(d.options) > 0 {
			out = append(out, sDim.Render(fit(" groups: "+strings.Join(d.options, ", "), w)))
		}
		out = append(out, sDim.Render("  ⏎ ok · esc cancel"))
	}
	return out
}

func dialogName(k dialogKind) string {
	switch k {
	case dlgNew:
		return "new agent"
	case dlgPreset:
		return "preset"
	case dlgRename, dlgRenameGroup:
		return "rename"
	case dlgGroup:
		return "group"
	case dlgMessage:
		return "message"
	case dlgConfirm:
		return "confirm"
	}
	return ""
}

func ringBar(win limits.Window, n int) string {
	if !win.Valid {
		return sFaint.Render(strings.Repeat("╌", n) + "  --")
	}
	filled := int(math.Round(win.Used / 100 * float64(n)))
	if win.Used > 0 && filled == 0 {
		filled = 1
	}
	if filled > n {
		filled = n
	}
	st := sAccent
	if win.Used >= 80 {
		st = sWarn
	}
	pct := fmt.Sprintf("%3.0f%%", win.Used)
	if win.Used >= 100 {
		pct = " ●  "
	}
	return st.Render(strings.Repeat("▰", filled)) + sFaint.Render(strings.Repeat("▱", n-filled)) + st.Render(pct)
}

func (m Model) limitLines() []string {
	if len(m.limits) == 0 {
		return nil
	}
	var out []string
	// " Claude 5h ▰▰▱▱▱ 42% wk ▰▰▰▰▱ 85%" needs 33 cells
	full := m.width >= 33
	for _, p := range m.limits {
		name := fmt.Sprintf(" %-6s ", ansi.Truncate(p.Name, 6, ""))
		if p.Stale {
			out = append(out, sDim.Render(name)+sFaint.Render("╌╌ stale"))
			continue
		}
		if full {
			out = append(out, sDim.Render(name+"5h ")+ringBar(p.Five, 5)+sDim.Render(" wk ")+ringBar(p.Week, 5))
		} else {
			out = append(out, sDim.Render(name)+ringBar(p.Five, 3)+" "+ringBar(p.Week, 3))
		}
	}
	return out
}

var helpKeys = [][2]string{
	{"↑↓ / j k", "move"},
	{"⏎", "open agent (focus it)"},
	{"space / l", "show agent, stay here"},
	{"tab / ⇧tab", "next / prev agent that needs you"},
	{"n", "new agent"},
	{"N", "new agent from preset"},
	{"a / A", "allow / deny (when it needs you)"},
	{"m", "send a message"},
	{"r", "rename agent or group"},
	{"g", "move to group"},
	{"J / K", "move down / up"},
	{"⏎ on group", "fold / unfold"},
	{"z", "set aside (moon)"},
	{"u", "toggle unread"},
	{"d", "review diff"},
	{"D", "remove worktree"},
	{"x", "close agent"},
	{"/", "filter"},
	{"p", "toggle preview"},
	{"q", "detach (agents keep running)"},
	{"Q", "close herd window"},
	{"prefix C-h", "back to the sidebar"},
	{"prefix C-j", "jump to next agent"},
}

func (m Model) helpView() string {
	w := m.width
	out := []string{" " + sTitle.Render("herd keys"), ""}
	for _, k := range helpKeys {
		out = append(out, fit(" "+sKey.Render(fmt.Sprintf("%-11s", k[0]))+" "+sText.Render(k[1]), w))
	}
	out = append(out, "", sDim.Render(" any key to close"))
	if len(out) > m.height {
		out = out[:m.height]
	}
	for len(out) < m.height {
		out = append(out, "")
	}
	for i := range out {
		out[i] = fit(out[i], w)
	}
	return strings.Join(out, "\n")
}
