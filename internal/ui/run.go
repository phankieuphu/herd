package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phankieuphu/herd/internal/config"
	"github.com/phankieuphu/herd/internal/core"
)

// Run starts the sidebar and blocks until it exits.
func Run(eng *core.Engine, cfg *config.Store) error {
	p := tea.NewProgram(New(eng, cfg), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if stop, err := config.Watch(func(c *config.Config, err error) {
		p.Send(ConfigMsg{Cfg: c, Err: err})
	}); err == nil {
		defer stop()
	}
	_, err := p.Run()
	return err
}
