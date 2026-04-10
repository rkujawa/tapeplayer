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

	// dimStyle replaces tapeStatusStyle — used for tape status, playlist
	// suffixes, scroll indicators, and the EOT/more footer.
	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	// currentTrackStyle highlights the currently playing playlist entry.
	currentTrackStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255"))

	// trackStyle for non-current playlist entries.
	trackStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	// separatorStyle for horizontal rules.
	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("236"))

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
