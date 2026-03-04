package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15"))

	criticalStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196"))

	warningStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220"))

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("45"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("237")).
			Foreground(lipgloss.Color("15"))

	dangerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196"))

	// connectorStyle renders tree connectors (┌├└──) in bright white without bold,
	// so box-drawing characters don't get stretched in terminals that widen bold glyphs.
	connectorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15"))

	// Selected variants: same foreground colors but with selected background
	selectedBg = lipgloss.Color("237")

	selectedConnectorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15")).
				Background(selectedBg)

	selectedDangerStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("196")).
				Background(selectedBg)

	selectedHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15")).
				Background(selectedBg)
)
