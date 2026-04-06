package ui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229"))

	statePlayingStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("82"))

	statePausedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("214"))

	stateStoppedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("196"))

	stateLoadingStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	valueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255"))

	tapeStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	progressFull  = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).SetString("━")
	progressEmpty = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).SetString("━")
)
