package player

import "time"

// State represents the player's current operational state.
type State int

const (
	Stopped State = iota
	Loading
	Playing
	Paused
)

// String returns a human-readable state name.
func (s State) String() string {
	switch s {
	case Stopped:
		return "Stopped"
	case Loading:
		return "Loading"
	case Playing:
		return "Playing"
	case Paused:
		return "Paused"
	default:
		return "Unknown"
	}
}

// TrackInfo holds metadata extracted from a FLAC file.
type TrackInfo struct {
	Artist     string
	Album      string
	Title      string
	TrackNum   string
	SampleRate uint32
	BitsPerSample uint8
	Channels   uint8
	TotalSamples uint64
}

// Duration returns the track duration computed from STREAMINFO.
func (ti TrackInfo) Duration() time.Duration {
	if ti.SampleRate == 0 {
		return 0
	}
	return time.Duration(ti.TotalSamples) * time.Second / time.Duration(ti.SampleRate)
}

// TapeStatus holds current tape drive status for display.
type TapeStatus struct {
	FileNumber  int
	BlockPos    uint32
	BytesRead   int64
	ReadRate    float64 // MB/s average since start
	CurrentRate float64 // MB/s over last 1-second window
	BufferBytes int
	Complete    bool
	Seeking     bool // true while skipping to next filemark
}

// --- Bubbletea messages sent from background goroutines ---

// StateChangedMsg signals a player state transition.
type StateChangedMsg struct{ State State }

// TrackInfoMsg carries FLAC metadata for the current track.
type TrackInfoMsg struct{ Info TrackInfo }

// TapeStatusMsg carries periodic tape status updates.
type TapeStatusMsg struct{ Status TapeStatus }

// PlaybackProgressMsg carries current playback position.
type PlaybackProgressMsg struct {
	Position time.Duration
	Duration time.Duration
}

// TrackEndMsg signals the current track finished playing.
type TrackEndMsg struct{}

// EOTMsg signals end of tape (double filemark).
type EOTMsg struct{}

// ErrorMsg carries an error from a background goroutine.
type ErrorMsg struct{ Err error }

// AudioErrorMsg is sent when the audio device fails during init or playback.
// The TUI can offer retry (re-init audio for current track) or skip (Forward).
type AudioErrorMsg struct{ Err error }

// PlaylistUpdateMsg carries the updated playlist for UI display.
type PlaylistUpdateMsg struct {
	Entries []PlaylistEntry
	Current int
	EOT     bool
}

// AudioDeviceInfo holds the negotiated audio output configuration.
type AudioDeviceInfo struct {
	DeviceName string // e.g., "Built-in Output", "default"
	SampleRate uint32 // negotiated sample rate
	Format     string // e.g., "S24LE", "S16LE"
	Channels   uint32 // number of output channels
}

// AudioInfoMsg carries audio device information after initialization.
type AudioInfoMsg struct{ Info AudioDeviceInfo }

// TickMsg is sent periodically to update the UI.
type TickMsg struct{}
