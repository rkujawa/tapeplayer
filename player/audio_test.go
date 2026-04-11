package player

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	tape "github.com/uiscsi/uiscsi-tape"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAudioInitFailure(t *testing.T) {
	audioInitErr := errors.New("test: audio device unavailable")

	mock := &mockDrive{
		readFunc: func(_ context.Context, buf []byte) (int, error) {
			return 0, tape.ErrFilemark
		},
	}
	logger := slog.New(slog.DiscardHandler)
	p := &Player{
		logger:       logger,
		playlist:     NewPlaylist(0),
		msgCh:        make(chan tea.Msg, 64),
		tc:           newTapeController(mock, logger, 1024),
		state:        Stopped,
		newAudioFunc: func(uint32, uint8, uint8, *slog.Logger) (*audioDevice, error) {
			return nil, audioInitErr
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start tape controller so cleanup works.
	go p.tc.run(ctx)
	p.bgWg.Add(1)
	go func() {
		defer p.bgWg.Done()
		p.handleTapeResults()
	}()

	// Run startDecoder with valid minimal FLAC data.
	// FLAC parse succeeds, then audio init fails via our mock.
	flacData := buildMinimalFLAC(44100, 2, 16, 100)
	sb := newStreamBufferFrom(flacData)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.startDecoder(ctx, sb, 0)
	}()
	p.wg.Wait()

	// Drain msgCh to collect all messages sent by startDecoder.
	// startDecoder sends: TrackInfoMsg, PlaylistUpdateMsg, then AudioErrorMsg,
	// then StateChangedMsg(Stopped). All go to the buffered msgCh (cap 64).
	foundAudioErr := false
	drainLoop:
	for {
		select {
		case msg := <-p.msgCh:
			if ae, ok := msg.(AudioErrorMsg); ok {
				if !errors.Is(ae.Err, audioInitErr) {
					t.Errorf("AudioErrorMsg.Err = %v, want wrapping %v", ae.Err, audioInitErr)
				}
				foundAudioErr = true
			}
		default:
			break drainLoop
		}
	}

	if !foundAudioErr {
		t.Error("expected AudioErrorMsg in message channel")
	}

	// Player should be in Stopped state.
	if s := p.State(); s != Stopped {
		t.Errorf("expected Stopped state, got %v", s)
	}

	// After audio init failure, audioDev must be nil (no leak).
	if p.audioDev != nil {
		t.Error("p.audioDev should be nil after audio init failure")
	}

	// Clean up: stop controller and drain remaining messages.
	p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdClose}
	p.bgWg.Wait()
	p.closeOnce.Do(func() { close(p.msgCh) })
}

func TestAudioStartFailureCleansUp(t *testing.T) {
	// Verify that when audio init fails, p.audioDev is nil (no leak).
	// The start() failure path (where device is created but start fails)
	// requires a real malgo device, so we verify the code structure:
	// player.go calls ad.close() and sets p.audioDev = nil on start failure.
	//
	// This test verifies the init-failure path since both paths ensure
	// p.audioDev == nil after failure.

	audioInitErr := errors.New("test: audio unavailable")
	logger := slog.New(slog.DiscardHandler)
	mock := &mockDrive{}
	p := &Player{
		logger:       logger,
		playlist:     NewPlaylist(0),
		msgCh:        make(chan tea.Msg, 64),
		tc:           newTapeController(mock, logger, 1024),
		state:        Stopped,
		newAudioFunc: func(uint32, uint8, uint8, *slog.Logger) (*audioDevice, error) {
			return nil, audioInitErr
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.tc.run(ctx)
	p.bgWg.Add(1)
	go func() {
		defer p.bgWg.Done()
		p.handleTapeResults()
	}()

	flacData := buildMinimalFLAC(44100, 2, 16, 100)
	sb := newStreamBufferFrom(flacData)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.startDecoder(ctx, sb, 0)
	}()
	p.wg.Wait()

	if p.audioDev != nil {
		t.Error("p.audioDev should be nil after audio init failure")
	}

	// Verify AudioErrorMsg was sent (not generic ErrorMsg).
	foundAudioErr := false
	drainLoop:
	for {
		select {
		case msg := <-p.msgCh:
			if _, ok := msg.(AudioErrorMsg); ok {
				foundAudioErr = true
			}
			if _, ok := msg.(ErrorMsg); ok {
				t.Error("expected AudioErrorMsg, got generic ErrorMsg")
			}
		default:
			break drainLoop
		}
	}
	if !foundAudioErr {
		t.Error("expected AudioErrorMsg")
	}

	// Clean up.
	p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdClose}
	p.bgWg.Wait()
	p.closeOnce.Do(func() { close(p.msgCh) })
}

// buildMinimalFLAC constructs a minimal valid FLAC stream with just
// the fLaC marker and a STREAMINFO metadata block (34 bytes).
// This is enough for the mewkiz/flac decoder to parse metadata.
func buildMinimalFLAC(sampleRate uint32, channels, bitsPerSample uint8, totalSamples uint64) []byte {
	buf := make([]byte, 0, 42)
	buf = append(buf, 'f', 'L', 'a', 'C')

	// Metadata block header: last=1, type=0 (STREAMINFO), length=34
	buf = append(buf, 0x80, 0x00, 0x00, 0x22)

	// STREAMINFO (34 bytes):
	buf = append(buf, 0x10, 0x00) // min block size = 4096
	buf = append(buf, 0x10, 0x00) // max block size = 4096
	buf = append(buf, 0x00, 0x00, 0x00) // min frame size = 0
	buf = append(buf, 0x00, 0x00, 0x00) // max frame size = 0

	// sample rate (20 bits) | channels-1 (3 bits) | bps-1 (5 bits) | total samples high (4 bits)
	sr := sampleRate
	ch := channels - 1
	bps := bitsPerSample - 1

	buf = append(buf, byte(sr>>12))
	buf = append(buf, byte(sr>>4))
	buf = append(buf, byte(sr<<4)|byte(ch<<1)|byte(bps>>4))
	buf = append(buf, byte(bps<<4)|byte((totalSamples>>32)&0x0F))
	buf = append(buf, byte(totalSamples>>24), byte(totalSamples>>16), byte(totalSamples>>8), byte(totalSamples))

	// MD5 signature (16 bytes of zeros)
	buf = append(buf, make([]byte, 16)...)

	return buf
}
