package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/uiscsi/tapeplayer/player"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Model is the bubbletea model for the tapeplayer TUI.
type Model struct {
	player          PlayerAPI
	ctx             context.Context
	state           player.State
	track           player.TrackInfo
	tape            player.TapeStatus
	position        time.Duration
	duration        time.Duration
	lastErr         string
	audioErr        bool
	audioErrMsg     string
	quitting        bool
	driveInfo       string
	audioDevice     string
	playlist        []player.PlaylistEntry
	playlistCurrent int
	playlistEOT     bool
	width           int
	height          int
}

// New creates the TUI model.
func New(p PlayerAPI, ctx context.Context, driveInfo string) Model {
	return Model{
		player:          p,
		ctx:             ctx,
		state:           player.Stopped,
		driveInfo:       driveInfo,
		playlistCurrent: -1,
		width:           80,
		height:          24,
	}
}

// Init returns the initial command (periodic tick).
func (Model) Init() tea.Cmd {
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(_ time.Time) tea.Msg {
		return player.TickMsg{}
	})
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case player.StateChangedMsg:
		m.state = msg.State
		m.audioErr = false
		m.audioErrMsg = ""
		return m, nil

	case player.AudioErrorMsg:
		m.audioErr = true
		m.audioErrMsg = msg.Err.Error()
		m.lastErr = ""
		return m, nil

	case player.TrackInfoMsg:
		m.track = msg.Info
		m.lastErr = ""
		return m, nil

	case player.TapeStatusMsg:
		m.tape = msg.Status
		return m, nil

	case player.AudioInfoMsg:
		m.audioDevice = msg.Info.DeviceName
		return m, nil

	case player.PlaybackProgressMsg:
		m.position = msg.Position
		m.duration = msg.Duration
		return m, nil

	case player.PlaylistUpdateMsg:
		m.playlist = msg.Entries
		m.playlistCurrent = msg.Current
		m.playlistEOT = msg.EOT
		return m, nil

	case player.TrackEndMsg:
		m.player.Forward(m.ctx)
		return m, nil

	case player.EOTMsg:
		m.player.Stop()
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
	// Context-sensitive keys when an audio error is active.
	if m.audioErr {
		switch msg.String() {
		case KeyRewind: // 'r' = retry current track when audio error active
			m.audioErr = false
			m.audioErrMsg = ""
			m.player.Play(m.ctx)
			return m, nil
		case "n": // skip to next track
			m.audioErr = false
			m.audioErrMsg = ""
			m.player.Forward(m.ctx)
			return m, nil
		}
	}

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

// innerWidth returns the usable content width inside the border.
// Border takes 2 chars (left+right) + padding 2 chars (1 each side).
func (m Model) innerWidth() int {
	w := m.width - 4
	if w < 76 {
		w = 76
	}
	return w
}

// View renders the TUI.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	inner := m.innerWidth()
	var b strings.Builder

	// Header: title + drive info + audio device.
	header := titleStyle.Render("tapeplayer") + "  " + dimStyle.Render(m.driveInfo)
	if m.audioDevice != "" {
		header += "  " + dimStyle.Render("▸ "+m.audioDevice)
	}
	b.WriteString(header + "\n")
	b.WriteString(separatorStyle.Render(strings.Repeat("─", inner)) + "\n")

	// State + time.
	stateIcon, stateStr := m.renderState()
	timeStr := fmt.Sprintf("[%s/%s]", formatDuration(m.position), formatDuration(m.duration))
	statePart := fmt.Sprintf("  %s %s", stateIcon, stateStr)
	gap := inner - lipgloss.Width(statePart) - lipgloss.Width(timeStr) - 2
	if gap < 1 {
		gap = 1
	}
	b.WriteString(statePart + strings.Repeat(" ", gap) + timeStr + "\n")

	// Tape status (below state line).
	tapeStr := m.renderTapeStatus()
	b.WriteString("  " + dimStyle.Render(tapeStr) + "\n\n")

	// Track metadata.
	if m.track.Artist != "" {
		fmt.Fprintf(&b, "  %s %s\n", labelStyle.Render("Artist:"), valueStyle.Render(m.track.Artist))
	}
	if m.track.Album != "" {
		fmt.Fprintf(&b, "  %s  %s\n", labelStyle.Render("Album:"), valueStyle.Render(m.track.Album))
	}
	if m.track.Title != "" {
		fmt.Fprintf(&b, "  %s  %s\n", labelStyle.Render("Title:"), valueStyle.Render(m.track.Title))
	}
	if m.track.SampleRate > 0 {
		fmt.Fprintf(&b, "  %s %s\n",
			labelStyle.Render("Format:"),
			valueStyle.Render(fmt.Sprintf("%d-bit %dHz %dch FLAC",
				m.track.BitsPerSample, m.track.SampleRate, m.track.Channels)))
	}
	b.WriteString("\n")

	// Progress bar — scale to available width.
	progWidth := inner - 8
	if progWidth < 20 {
		progWidth = 20
	}
	b.WriteString("  " + m.renderProgress(progWidth) + "\n\n")

	// Playlist (scrollable).
	if len(m.playlist) > 0 {
		// Calculate fixed lines used by non-playlist content.
		// header(1) + separator(1) + state(1) + tape(1) + blank(1) +
		// blank after metadata(1) + progress(1) + blank(1) +
		// blank after playlist(1) + help(1) + border(2) = 12
		fixedLines := 12
		if m.track.Artist != "" {
			fixedLines++
		}
		if m.track.Album != "" {
			fixedLines++
		}
		if m.track.Title != "" {
			fixedLines++
		}
		if m.track.SampleRate > 0 {
			fixedLines++
		}
		if m.audioErr {
			fixedLines += 3 // error line + hint line + blank
		} else if m.lastErr != "" {
			fixedLines += 2
		}

		maxPlaylistLines := m.height - fixedLines
		if maxPlaylistLines < 3 {
			maxPlaylistLines = 3
		}

		b.WriteString(m.renderPlaylist(maxPlaylistLines))
		b.WriteString("\n")
	}

	// Error.
	if m.audioErr {
		b.WriteString("  " + errorStyle.Render("Audio error: "+m.audioErrMsg) + "\n")
		b.WriteString("  " + helpStyle.Render("[r] retry  [n] skip to next") + "\n\n")
	} else if m.lastErr != "" {
		b.WriteString("  " + errorStyle.Render(m.lastErr) + "\n\n")
	}

	// Help.
	b.WriteString("  " + helpStyle.Render("[space] play/pause  [f] next  [b] prev  [s] stop  [r] rewind  [q] quit") + "\n")

	content := b.String()
	style := borderStyle.Width(m.width - 2).Height(m.height - 2)
	return style.Render(content)
}

func (m Model) renderTapeStatus() string {
	if m.tape.Seeking {
		return "Tape: seeking to next track..."
	}
	if !m.tape.Complete && m.tape.BytesRead > 0 {
		s := fmt.Sprintf("Tape: %.1f MB loading...", float64(m.tape.BytesRead)/1e6)
		if m.tape.CurrentRate > 0 {
			s += fmt.Sprintf(" | %.1f MB/s", m.tape.CurrentRate)
		}
		if m.tape.ReadRate > 0 {
			s += fmt.Sprintf(" (avg %.1f MB/s)", m.tape.ReadRate)
		}
		return s
	}
	return "Tape: idle"
}

func (m Model) renderPlaylist(maxLines int) string {
	totalEntries := len(m.playlist)
	footerLine := 1 // "-- end of tape --" or "-- more on tape --"
	totalLines := totalEntries + footerLine

	visibleEntries := maxLines - footerLine
	if visibleEntries > totalEntries {
		visibleEntries = totalEntries
	}
	if visibleEntries < 1 {
		visibleEntries = 1
	}

	startIdx := 0
	needScroll := visibleEntries < totalEntries
	if needScroll {
		// Center the current track in the visible window.
		cur := m.playlistCurrent
		if cur < 0 {
			cur = 0
		}
		// Reserve lines for scroll indicators.
		startIdx = cur - visibleEntries/2
		if startIdx < 0 {
			startIdx = 0
		}
		if startIdx+visibleEntries > totalEntries {
			startIdx = totalEntries - visibleEntries
		}
	}
	_ = totalLines

	var b strings.Builder
	endIdx := startIdx + visibleEntries

	if needScroll && startIdx > 0 {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("    ↑ %d more", startIdx)) + "\n")
		startIdx++
	}

	for i := startIdx; i < endIdx; i++ {
		e := m.playlist[i]
		prefix := "  "
		if e.Index == m.playlistCurrent {
			prefix = " ▶"
		}

		title := e.Info.Title
		if title == "" {
			title = fmt.Sprintf("Track %d", e.Index+1)
		}

		var suffix string
		switch {
		case e.Cached:
			suffix = dimStyle.Render(fmt.Sprintf(" [%.1f MB, cached]", float64(e.Size)/1e6))
		case e.Partial:
			suffix = dimStyle.Render(fmt.Sprintf(" [%.1f MB, partial]", float64(e.Size)/1e6))
		default:
			suffix = dimStyle.Render(fmt.Sprintf(" [%.1f MB, on tape]", float64(e.Size)/1e6))
		}

		line := fmt.Sprintf("%s %d. %s%s", prefix, e.Index+1, title, suffix)
		if e.Index == m.playlistCurrent {
			b.WriteString("  " + currentTrackStyle.Render(line) + "\n")
		} else {
			b.WriteString("  " + trackStyle.Render(line) + "\n")
		}
	}

	if needScroll && endIdx < totalEntries {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("    ↓ %d more", totalEntries-endIdx)) + "\n")
	}

	if m.playlistEOT {
		b.WriteString("  " + dimStyle.Render("  ── end of tape ──") + "\n")
	} else if len(m.playlist) > 0 {
		b.WriteString("  " + dimStyle.Render("  ── more on tape ──") + "\n")
	}
	return b.String()
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
