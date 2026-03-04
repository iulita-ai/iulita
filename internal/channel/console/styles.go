package console

import "github.com/charmbracelet/lipgloss"

type uiStyles struct {
	userLabel     lipgloss.Style
	botLabel      lipgloss.Style
	userMsg       lipgloss.Style
	botMsg        lipgloss.Style
	statusLine    lipgloss.Style
	errorLine     lipgloss.Style
	divider       lipgloss.Style
	statusBar     lipgloss.Style
	skillBadge    lipgloss.Style
	thinkBadge    lipgloss.Style
	helpKey       lipgloss.Style
	helpDesc      lipgloss.Style
}

func defaultStyles() uiStyles {
	return uiStyles{
		userLabel: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("6")), // cyan
		botLabel: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("5")), // magenta
		userMsg: lipgloss.NewStyle().
			Foreground(lipgloss.Color("7")), // white
		botMsg: lipgloss.NewStyle(),
		statusLine: lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")), // gray
		errorLine: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("1")), // red
		divider: lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")),
		statusBar: lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")).
			Background(lipgloss.Color("0")),
		skillBadge: lipgloss.NewStyle().
			Foreground(lipgloss.Color("3")), // yellow
		thinkBadge: lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")).
			Italic(true),
		helpKey: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("6")),
		helpDesc: lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")),
	}
}
