package ui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/phankieuphu/herd/internal/model"
)

var (
	cDim      = lipgloss.AdaptiveColor{Light: "245", Dark: "243"}
	cFaint    = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	cText     = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	cAccent   = lipgloss.AdaptiveColor{Light: "25", Dark: "75"}
	cWork     = lipgloss.AdaptiveColor{Light: "136", Dark: "221"}
	cNeed     = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	cDone     = lipgloss.AdaptiveColor{Light: "28", Dark: "114"}
	cBG       = lipgloss.AdaptiveColor{Light: "30", Dark: "80"}
	cSelBg    = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}
	cAdd      = lipgloss.AdaptiveColor{Light: "28", Dark: "114"}
	cDel      = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	cWarnRing = lipgloss.AdaptiveColor{Light: "130", Dark: "215"}

	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sText    = lipgloss.NewStyle().Foreground(cText)
	sBold    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sAccent  = lipgloss.NewStyle().Foreground(cAccent)
	sHeader  = lipgloss.NewStyle().Foreground(cDim).Bold(true)
	sStage   = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sUnread  = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sAdd     = lipgloss.NewStyle().Foreground(cAdd)
	sDel     = lipgloss.NewStyle().Foreground(cDel)
	sErr     = lipgloss.NewStyle().Foreground(cNeed)
	sOK      = lipgloss.NewStyle().Foreground(cDone)
	sWarn    = lipgloss.NewStyle().Foreground(cWarnRing).Bold(true)
	sKey     = lipgloss.NewStyle().Foreground(cAccent)
	sSelLine = lipgloss.NewStyle().Background(cSelBg)
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var bgSpinner = []string{"◐", "◓", "◑", "◒"}

// icon returns the state symbol and its style.
func icon(s model.State, inactive bool, frame int) string {
	if inactive {
		return sDim.Render("☾")
	}
	switch s {
	case model.Working:
		return lipgloss.NewStyle().Foreground(cWork).Render(spinner[frame%len(spinner)])
	case model.Background:
		return lipgloss.NewStyle().Foreground(cBG).Render(bgSpinner[(frame/2)%len(bgSpinner)])
	case model.NeedsYou:
		return lipgloss.NewStyle().Foreground(cNeed).Bold(true).Render("◆")
	case model.Done:
		return lipgloss.NewStyle().Foreground(cDone).Render("✓")
	case model.Exited:
		return sDim.Render("○")
	}
	return sFaint.Render("·")
}

func stateWord(s model.State) string {
	switch s {
	case model.Working:
		return "working"
	case model.Background:
		return "background"
	case model.NeedsYou:
		return "needs you"
	case model.Done:
		return "done"
	case model.Exited:
		return "exited"
	}
	return "…"
}
