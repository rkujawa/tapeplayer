package player

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	tape "github.com/uiscsi/uiscsi-tape"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// mockDrive implements driveOps for testing.
type mockDrive struct {
	mu          sync.Mutex
	readFunc    func(ctx context.Context, buf []byte) (int, error)
	rewindFunc  func(ctx context.Context) error
	readCalls   int
	rewindCalls int
}

func (m *mockDrive) Read(ctx context.Context, buf []byte) (int, error) {
	m.mu.Lock()
	m.readCalls++
	f := m.readFunc
	m.mu.Unlock()
	if f != nil {
		return f(ctx, buf)
	}
	return 0, tape.ErrFilemark
}

func (m *mockDrive) Rewind(ctx context.Context) error {
	m.mu.Lock()
	m.rewindCalls++
	f := m.rewindFunc
	m.mu.Unlock()
	if f != nil {
		return f(ctx)
	}
	return nil
}

func TestTapeControllerWithMockDrive(t *testing.T) {
	// Mock returns 4 bytes of data on first Read, then ErrFilemark.
	var callCount sync.Mutex
	calls := 0
	mock := &mockDrive{
		readFunc: func(_ context.Context, buf []byte) (int, error) {
			callCount.Lock()
			calls++
			n := calls
			callCount.Unlock()
			if n == 1 {
				// Return some data so sendFileResult produces a result.
				copy(buf, []byte("data"))
				return 4, nil
			}
			return 0, tape.ErrFilemark
		},
	}
	tc := newTapeController(mock, discardLogger(), 1024)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run controller in background.
	done := make(chan struct{})
	go func() {
		tc.run(ctx)
		close(done)
	}()

	// Send a read command with a streamBuffer.
	sb := newStreamBuffer()
	tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRead, sb: sb, index: 0}

	// Mock returns data then ErrFilemark, producing a tapeResultFileComplete.
	select {
	case result := <-tc.resultCh:
		if result.kind != tapeResultFileComplete {
			t.Fatalf("expected tapeResultFileComplete, got %d", result.kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tape result")
	}

	// Send rewind command.
	tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRewind}
	select {
	case result := <-tc.resultCh:
		if result.kind != tapeResultRewound {
			t.Fatalf("expected tapeResultRewound, got %d", result.kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for rewind result")
	}

	mock.mu.Lock()
	reads := mock.readCalls
	rewinds := mock.rewindCalls
	mock.mu.Unlock()

	if reads == 0 {
		t.Error("expected at least one Read call")
	}
	if rewinds != 1 {
		t.Errorf("expected 1 Rewind call, got %d", rewinds)
	}

	// Send close command — controller should exit.
	tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdClose}
	select {
	case <-done:
		// Controller exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for controller to exit")
	}

	// resultCh should be closed after run() returns.
	_, open := <-tc.resultCh
	if open {
		t.Error("resultCh should be closed after controller exit")
	}
}

func TestPlayerCloseIdempotent(t *testing.T) {
	mock := &mockDrive{}
	logger := discardLogger()
	p := &Player{
		logger:   logger,
		playlist: NewPlaylist(0),
		msgCh:    make(chan tea.Msg, 64),
		tc:       newTapeController(mock, logger, 1024),
		state:    Stopped,
	}

	// Start drainer goroutine (simulates SetProgram's drainer).
	drainerDone := make(chan struct{})
	go func() {
		for range p.msgCh {
		}
		close(drainerDone)
	}()

	// Start tape controller.
	go p.tc.run(context.Background())
	p.bgWg.Add(1)
	go func() {
		defer p.bgWg.Done()
		p.handleTapeResults()
	}()

	// Close twice — should not panic or deadlock.
	p.Close()
	p.Close()

	// Verify drainer exited (msgCh closed).
	select {
	case <-drainerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message drainer to exit")
	}
}
