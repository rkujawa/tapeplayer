package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/uiscsi/tapeplayer/player"

	tea "github.com/charmbracelet/bubbletea"
)

// mockPlayerAPI records method calls for assertion in tests.
type mockPlayerAPI struct {
	playCalled      bool
	pauseCalled     bool
	stopCalled      bool
	forwardCalled   bool
	backCalled      bool
	rewindCalled    bool
	closeCalled     bool
	toggleCalled    bool
}

func (m *mockPlayerAPI) Play(_ context.Context)             { m.playCalled = true }
func (m *mockPlayerAPI) Pause()                            { m.pauseCalled = true }
func (m *mockPlayerAPI) TogglePlayPause(_ context.Context) { m.toggleCalled = true }
func (m *mockPlayerAPI) Stop()                             { m.stopCalled = true }
func (m *mockPlayerAPI) Forward(_ context.Context)         { m.forwardCalled = true }
func (m *mockPlayerAPI) Back(_ context.Context)            { m.backCalled = true }
func (m *mockPlayerAPI) RewindTape(_ context.Context)      { m.rewindCalled = true }
func (m *mockPlayerAPI) Close()                            { m.closeCalled = true }
func (m *mockPlayerAPI) State() player.State               { return player.Stopped }
func (m *mockPlayerAPI) Playlist() *player.Playlist        { return nil }

func newTestModel(mock *mockPlayerAPI) Model {
	return New(mock, context.Background(), "test-drive")
}

// TestAudioErrorMsgSetsState verifies AudioErrorMsg sets audioErr and audioErrMsg.
func TestAudioErrorMsgSetsState(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)

	result, _ := m.Update(player.AudioErrorMsg{Err: errors.New("device busy")})
	updated := result.(Model)

	if !updated.audioErr {
		t.Error("audioErr should be true after AudioErrorMsg")
	}
	if updated.audioErrMsg != "device busy" {
		t.Errorf("audioErrMsg = %q, want %q", updated.audioErrMsg, "device busy")
	}
	if updated.lastErr != "" {
		t.Errorf("lastErr should be cleared, got %q", updated.lastErr)
	}
}

// TestAudioErrorMsgView verifies View() contains the error text and key hints.
func TestAudioErrorMsgView(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)
	m.width = 120
	m.height = 40

	result, _ := m.Update(player.AudioErrorMsg{Err: errors.New("output device failed")})
	updated := result.(Model)

	view := updated.View()
	if !strings.Contains(view, "output device failed") {
		t.Errorf("View() should contain error text, got:\n%s", view)
	}
	if !strings.Contains(view, "[r] retry") {
		t.Errorf("View() should contain retry hint, got:\n%s", view)
	}
	if !strings.Contains(view, "[n] skip to next") {
		t.Errorf("View() should contain skip hint, got:\n%s", view)
	}
}

// TestAudioErrorRetryKey verifies 'r' when audioErr=true calls Play and clears error state.
func TestAudioErrorRetryKey(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)
	m.audioErr = true
	m.audioErrMsg = "device busy"

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	updated := result.(Model)

	if !mock.playCalled {
		t.Error("Play() should be called when 'r' is pressed with audioErr=true")
	}
	if mock.rewindCalled {
		t.Error("RewindTape() should NOT be called when audioErr=true and 'r' pressed")
	}
	if updated.audioErr {
		t.Error("audioErr should be cleared after retry")
	}
	if updated.audioErrMsg != "" {
		t.Errorf("audioErrMsg should be cleared after retry, got %q", updated.audioErrMsg)
	}
}

// TestAudioErrorSkipKey verifies 'n' when audioErr=true calls Forward and clears error state.
func TestAudioErrorSkipKey(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)
	m.audioErr = true
	m.audioErrMsg = "device busy"

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	updated := result.(Model)

	if !mock.forwardCalled {
		t.Error("Forward() should be called when 'n' is pressed with audioErr=true")
	}
	if updated.audioErr {
		t.Error("audioErr should be cleared after skip")
	}
	if updated.audioErrMsg != "" {
		t.Errorf("audioErrMsg should be cleared after skip, got %q", updated.audioErrMsg)
	}
}

// TestRewindKeyNormal verifies 'r' when audioErr=false calls RewindTape (not Play).
func TestRewindKeyNormal(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)
	// audioErr is false by default

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	_ = result

	if !mock.rewindCalled {
		t.Error("RewindTape() should be called when 'r' is pressed with audioErr=false")
	}
	if mock.playCalled {
		t.Error("Play() should NOT be called when audioErr=false and 'r' pressed")
	}
}

// TestStateChangedClearsAudioErr verifies StateChangedMsg clears audioErr.
func TestStateChangedClearsAudioErr(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)
	m.audioErr = true
	m.audioErrMsg = "device busy"

	result, _ := m.Update(player.StateChangedMsg{State: player.Playing})
	updated := result.(Model)

	if updated.audioErr {
		t.Error("audioErr should be cleared by StateChangedMsg")
	}
	if updated.audioErrMsg != "" {
		t.Errorf("audioErrMsg should be cleared by StateChangedMsg, got %q", updated.audioErrMsg)
	}
	if updated.state != player.Playing {
		t.Errorf("state = %v, want Playing", updated.state)
	}
}

// TestSkipKeyNoopWhenNoError verifies 'n' when audioErr=false does nothing.
func TestSkipKeyNoopWhenNoError(t *testing.T) {
	mock := &mockPlayerAPI{}
	m := newTestModel(mock)
	// audioErr is false by default

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})

	if mock.forwardCalled {
		t.Error("Forward() should NOT be called when audioErr=false and 'n' pressed")
	}
	if mock.playCalled {
		t.Error("Play() should NOT be called when audioErr=false and 'n' pressed")
	}
}
