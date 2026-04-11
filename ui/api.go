// Package ui provides the bubbletea TUI model for tapeplayer.
package ui

import (
	"context"

	"github.com/uiscsi/tapeplayer/player"
)

// PlayerAPI defines the interface between the TUI and the player engine.
// This decouples the UI from the concrete Player type, making the
// interaction explicit and testable.
type PlayerAPI interface {
	Play(ctx context.Context)
	Pause()
	TogglePlayPause(ctx context.Context)
	Stop()
	Forward(ctx context.Context)
	Back(ctx context.Context)
	RewindTape(ctx context.Context)
	Close()
	State() player.State
	Playlist() *player.Playlist
}
