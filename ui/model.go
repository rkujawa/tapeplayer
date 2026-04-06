package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rkujawa/tapeplayer/player"

	tea "github.com/charmbracelet/bubbletea"
)

// Model is the bubbletea model for the tapeplayer TUI.
type Model struct {
	player     *player.Player
	ctx        context.Context
	state      player.State
	track      player.TrackInfo
	tape       player.TapeStatus
	position   time.Duration
	duration   time.Duration
	lastErr    string
	quitting   bool
	driveInfo  string
}

// New creates the TUI model.
func New(p *player.Player, ctx context.Context, driveInfo string) Model {
	return Model{
		player:    p,
		ctx:       ctx,
		state:     player.Stopped,
		driveInfo: driveInfo,
	}
}

// Init returns the initial command (periodic tick).
func (m Model) Init() tea.Cmd {
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg {
		return player.TickMsg{}
	})
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		return m.handleKey(msg)

	case player.StateChangedMsg:
		m.state = msg.State
		return m, nil

	case player.TrackInfoMsg:
		m.track = msg.Info
		m.lastErr = ""
		return m, nil

	case player.TapeStatusMsg:
		m.tape = msg.Status
		return m, nil

	case player.PlaybackProgressMsg:
		m.position = msg.Position
		m.duration = msg.Duration
		return m, nil

	case player.TrackEndMsg:
		// Auto-advance to next file.
		m.player.Forward(m.ctx)
		return m, nil

	case player.EOTMsg:
		m.state = player.Stopped
		m.lastErr = "End of tape"
		return m, nil

	case player.ErrorMsg:
		m.lastErr = msg.Err.Error()
		return m, nil

	case player.TickMsg:
		return m, tick()
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case KeyQuit, "ctrl+c":
		m.quitting = true
		m.player.Close()
		return m, tea.Quit

	case KeyPlayPause, "enter":
		m.player.TogglePlayPause(m.ctx)

	case KeyStop:
		m.player.Stop()

	case KeyForward, "right":
		m.player.Forward(m.ctx)

	case KeyBack, "left":
		m.player.Back(m.ctx)

	case KeyRewind:
		m.player.RewindTape(m.ctx)
	}

	return m, nil
}

// View renders the TUI.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Title.
	b.WriteString(titleStyle.Render("tapeplayer") + "  " + tapeStatusStyle.Render(m.driveInfo) + "\n\n")

	// State + time.
	stateIcon, stateStr := m.renderState()
	timeStr := fmt.Sprintf("[%s/%s]", formatDuration(m.position), formatDuration(m.duration))
	b.WriteString(fmt.Sprintf("  %s %s %34s\n\n", stateIcon, stateStr, timeStr))

	// Track metadata.
	if m.track.Artist != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Artist:"), valueStyle.Render(m.track.Artist)))
	}
	if m.track.Album != "" {
		b.WriteString(fmt.Sprintf("  %s  %s\n", labelStyle.Render("Album:"), valueStyle.Render(m.track.Album)))
	}
	if m.track.Title != "" {
		b.WriteString(fmt.Sprintf("  %s  %s\n", labelStyle.Render("Track:"), valueStyle.Render(m.track.Title)))
	}
	if m.track.SampleRate > 0 {
		b.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render("Format:"),
			valueStyle.Render(fmt.Sprintf("%d-bit %dHz %dch FLAC",
				m.track.BitsPerSample, m.track.SampleRate, m.track.Channels))))
	}
	b.WriteString("\n")

	// Progress bar.
	b.WriteString("  " + m.renderProgress(40) + "\n\n")

	// Tape status.
	tapeStr := fmt.Sprintf("Tape: File %d", m.tape.FileNumber)
	if m.tape.BytesRead > 0 {
		tapeStr += fmt.Sprintf(" | %.1f MB", float64(m.tape.BytesRead)/1e6)
		if !m.tape.Complete {
			tapeStr += " loading..."
		}
	}
	if m.tape.ReadRate > 0 {
		tapeStr += fmt.Sprintf(" | %.1f MB/s", m.tape.ReadRate)
	}
	b.WriteString("  " + tapeStatusStyle.Render(tapeStr) + "\n\n")

	// Error.
	if m.lastErr != "" {
		b.WriteString("  " + errorStyle.Render(m.lastErr) + "\n\n")
	}

	// Help.
	b.WriteString("  " + helpStyle.Render("[space] play/pause  [f] next  [b] prev  [s] stop  [r] rewind  [q] quit") + "\n")

	return borderStyle.Render(b.String())
}

func (m Model) renderState() (string, string) {
	switch m.state {
	case player.Playing:
		return "▶", statePlayingStyle.Render("Playing")
	case player.Paused:
		return "⏸", statePausedStyle.Render("Paused")
	case player.Loading:
		return "⟳", stateLoadingStyle.Render("Loading")
	default:
		return "⏹", stateStoppedStyle.Render("Stopped")
	}
}

func (m Model) renderProgress(width int) string {
	if m.duration == 0 {
		return strings.Repeat(progressEmpty.String(), width)
	}
	pct := float64(m.position) / float64(m.duration)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	return strings.Repeat(progressFull.String(), filled) +
		strings.Repeat(progressEmpty.String(), width-filled) +
		fmt.Sprintf(" %d%%", int(pct*100))
}

func formatDuration(d time.Duration) string {
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}
