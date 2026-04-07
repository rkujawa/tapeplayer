package player

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tape "github.com/rkujawa/uiscsi-tape"

	tea "github.com/charmbracelet/bubbletea"
)

// Player coordinates tape reading, FLAC decoding, and audio playback.
// It runs background goroutines and communicates with the bubbletea
// TUI via Program.Send.
type Player struct {
	drive     *tape.Drive
	logger    *slog.Logger
	prog      *tea.Program
	readBuf   int // tape read buffer size in bytes

	mu        sync.Mutex
	wg        sync.WaitGroup // tracks readFromTape and startDecoder goroutines
	state     State
	fileNum   int            // current file number on tape (1-based)
	cancel    context.CancelFunc
	audioDev  *audioDevice
	history   [][]byte       // buffered files for "previous" (last 3)

	// Current track state (valid during Loading/Playing/Paused).
	streamBuf *streamBuffer
	track     TrackInfo
	decoder   *flacDecoder
	startTime time.Time
	bytesRead int64
}

const maxHistory = 3

// New creates a Player for the given tape drive. readBufSize sets the
// tape read buffer size (must be >= the drive's configured block size
// for fixed-block mode). Use 0 for a sensible default (256KB).
func New(drive *tape.Drive, logger *slog.Logger, readBufSize int) *Player {
	if readBufSize <= 0 {
		readBufSize = 262144 // 256KB default
	}
	return &Player{
		drive:   drive,
		logger:  logger,
		readBuf: readBufSize,
		state:   Stopped,
	}
}

// SetProgram sets the bubbletea program for sending UI messages.
// Must be called before Play.
func (p *Player) SetProgram(prog *tea.Program) {
	p.prog = prog
}

// State returns the current player state.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *Player) setState(s State) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
	p.sendMsg(StateChangedMsg{State: s})
}

func (p *Player) sendMsg(msg tea.Msg) {
	if p.prog != nil {
		go p.prog.Send(msg)
	}
}

// Play starts or resumes playback.
func (p *Player) Play(ctx context.Context) {
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()

	p.logger.Debug("player: Play called", "state", state)

	switch state {
	case Stopped:
		p.startTrack(ctx)
	case Paused:
		p.resume()
	case Playing, Loading:
		// Already playing/loading — ignore.
	}
}

// Pause pauses playback.
func (p *Player) Pause() {
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()

	if state == Playing {
		p.setState(Paused)
		if p.audioDev != nil {
			p.audioDev.pause()
		}
	}
}

// TogglePlayPause toggles between play and pause.
func (p *Player) TogglePlayPause(ctx context.Context) {
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()

	p.logger.Debug("player: TogglePlayPause called", "state", state)

	switch state {
	case Playing:
		p.Pause()
	case Paused:
		p.resume()
	case Stopped:
		p.Play(ctx)
	}
}

// Stop stops playback and waits for background goroutines to exit.
func (p *Player) Stop() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mu.Unlock()

	if p.audioDev != nil {
		p.audioDev.stop()
	}
	p.wg.Wait() // wait for readFromTape + startDecoder to exit
	p.setState(Stopped)
}

// Forward skips to the next file on tape.
func (p *Player) Forward(ctx context.Context) {
	p.Stop()
	// The tape reader goroutine reads past the current file's remaining
	// data + filemark. We just start a new track — the tape is already
	// positioned at the next file if the reader finished, or we need
	// to skip. For simplicity, start a new track which reads from the
	// current tape position.
	p.startTrack(ctx)
}

// Back restarts the current track. If called within the first 3 seconds,
// goes to the previous track (from history buffer).
func (p *Player) Back(ctx context.Context) {
	p.mu.Lock()
	elapsed := time.Since(p.startTime)
	histLen := len(p.history)
	p.mu.Unlock()

	p.Stop()

	if elapsed < 3*time.Second && histLen > 0 {
		// Go to previous track from history.
		p.mu.Lock()
		prev := p.history[histLen-1]
		p.history = p.history[:histLen-1]
		p.fileNum--
		p.mu.Unlock()
		p.startTrackFromBuffer(ctx, prev)
	} else {
		// Restart current track from buffer if available.
		p.mu.Lock()
		buf := p.streamBuf
		p.mu.Unlock()

		if buf != nil && buf.IsComplete() {
			p.startTrackFromBuffer(ctx, buf.Bytes())
		}
		// If buffer not complete, can't restart — just stay stopped.
	}
}

// RewindTape rewinds the tape to BOT.
func (p *Player) RewindTape(ctx context.Context) {
	p.Stop()
	p.mu.Lock()
	p.fileNum = 0
	p.history = nil
	p.mu.Unlock()

	if err := p.drive.Rewind(ctx); err != nil {
		p.sendMsg(ErrorMsg{Err: fmt.Errorf("rewind: %w", err)})
	}
}

// Close releases audio resources. Safe to call from the bubbletea
// Update loop — does not send messages via prog.Send.
func (p *Player) Close() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.state = Stopped
	p.mu.Unlock()

	if p.audioDev != nil {
		p.audioDev.stop()
		p.audioDev.close()
		p.audioDev = nil
	}
}

func (p *Player) resume() {
	p.setState(Playing)
	if p.audioDev != nil {
		p.audioDev.resume()
	}
}

func (p *Player) startTrack(ctx context.Context) {
	p.logger.Debug("player: startTrack")
	trackCtx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	// Save current track to history if complete.
	if p.streamBuf != nil && p.streamBuf.IsComplete() {
		if len(p.history) >= maxHistory {
			p.history = p.history[1:]
		}
		p.history = append(p.history, p.streamBuf.Bytes())
	}
	p.cancel = cancel
	p.fileNum++
	p.streamBuf = newStreamBuffer()
	p.decoder = nil
	p.bytesRead = 0
	p.startTime = time.Now()
	sb := p.streamBuf
	fileNum := p.fileNum
	p.mu.Unlock()

	p.setState(Loading)

	// Start tape reader goroutine.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.readFromTape(trackCtx, sb, fileNum)
	}()
}

func (p *Player) startTrackFromBuffer(ctx context.Context, data []byte) {
	trackCtx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	p.cancel = cancel
	// Create a pre-filled, already-complete streamBuffer.
	sb := newStreamBuffer()
	sb.Write(data)
	sb.Complete()
	p.streamBuf = sb
	p.startTime = time.Now()
	p.mu.Unlock()

	// Skip tape reading — go straight to decode in a goroutine.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.startDecoder(trackCtx, sb)
	}()
}

func (p *Player) readFromTape(ctx context.Context, sb *streamBuffer, fileNum int) {
	p.logger.Debug("tape: reading file", "fileNum", fileNum, "readBuf", p.readBuf)
	buf := make([]byte, p.readBuf)
	readStart := time.Now()

	for {
		select {
		case <-ctx.Done():
			sb.Abort(ctx.Err())
			return
		default:
		}

		n, err := p.drive.Read(ctx, buf)
		if err != nil {
			if errors.Is(err, tape.ErrFilemark) {
				p.logger.Debug("tape: filemark", "fileNum", fileNum, "bytesRead", sb.Len())
				sb.Complete()
				p.sendMsg(TapeStatusMsg{Status: TapeStatus{
					FileNumber:  fileNum,
					BytesRead:   int64(sb.Len()),
					BufferBytes: sb.Len(),
					Complete:    true,
				}})
				return
			}
			if errors.Is(err, tape.ErrBlankCheck) {
				p.logger.Debug("tape: blank check (EOT)", "fileNum", fileNum)
				sb.Complete()
				p.sendMsg(EOTMsg{})
				return
			}
			if errors.Is(err, tape.ErrILI) && n > 0 {
				// Short record — normal for the last record before a filemark.
				p.logger.Debug("tape: short record (ILI)", "n", n)
				// Fall through to write the data we got.
			} else {
				p.logger.Error("tape: read error", "err", err)
				sb.Abort(err)
				p.sendMsg(ErrorMsg{Err: err})
				return
			}
		}

		if n > 0 {
			sb.Write(buf[:n])
			p.mu.Lock()
			p.bytesRead += int64(n)
			br := p.bytesRead
			p.mu.Unlock()

			elapsed := time.Since(readStart).Seconds()
			rate := 0.0
			if elapsed > 0 {
				rate = float64(br) / elapsed / 1e6
			}
			p.sendMsg(TapeStatusMsg{Status: TapeStatus{
				FileNumber:  fileNum,
				BytesRead:   br,
				ReadRate:    rate,
				BufferBytes: sb.Len(),
				Complete:    false,
			}})
		}

		// Start decoder once we have enough data for FLAC metadata
		// (typically a few KB). Launch exactly once.
		p.mu.Lock()
		needStart := p.decoder == nil && sb.Len() > 0
		if needStart {
			p.decoder = &flacDecoder{} // placeholder to prevent re-launch
		}
		p.mu.Unlock()
		if needStart {
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.startDecoder(ctx, sb)
			}()
		}
	}
}

func (p *Player) startDecoder(ctx context.Context, sb *streamBuffer) {
	p.logger.Debug("decoder: starting")

	dec, err := newFlacDecoder(sb, p.logger)
	if err != nil {
		p.logger.Error("decoder: init failed", "err", err)
		p.sendMsg(ErrorMsg{Err: fmt.Errorf("FLAC decode init: %w", err)})
		p.setState(Stopped)
		return
	}

	p.mu.Lock()
	p.decoder = dec
	p.track = dec.trackInfo()
	info := p.track
	p.mu.Unlock()

	p.sendMsg(TrackInfoMsg{Info: info})

	// Initialize audio device if needed.
	if p.audioDev == nil {
		ad, err := newAudioDevice(info.SampleRate, info.Channels, info.BitsPerSample, p.logger)
		if err != nil {
			p.sendMsg(ErrorMsg{Err: fmt.Errorf("audio init: %w", err)})
			p.setState(Stopped)
			return
		}
		p.audioDev = ad
	}

	p.audioDev.reset()
	p.setState(Playing)
	if err := p.audioDev.start(); err != nil {
		p.logger.Error("audio: start failed", "err", err)
		p.sendMsg(ErrorMsg{Err: fmt.Errorf("audio start: %w", err)})
		p.setState(Stopped)
		return
	}

	// Decode loop: decode FLAC frames → convert to PCM → write to ring buffer.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// If paused, wait briefly.
		if p.State() == Paused {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		samples, err := dec.nextFrame()
		if err != nil {
			if err.Error() == "EOF" {
				// Track finished.
				p.logger.Debug("decoder: track complete")
				// Drain audio buffer before signaling end.
				for p.audioDev.ring.Available() > 0 {
					time.Sleep(10 * time.Millisecond)
				}
				p.sendMsg(TrackEndMsg{})
				return
			}
			p.logger.Error("decoder: frame error", "err", err)
			p.sendMsg(ErrorMsg{Err: fmt.Errorf("FLAC decode: %w", err)})
			p.setState(Stopped)
			return
		}

		if len(samples) > 0 {
			if _, err := p.audioDev.ring.Write(samples); err != nil {
				// Ring buffer closed — stop requested.
				p.logger.Debug("decoder: ring buffer closed, stopping")
				return
			}

			// Send playback progress.
			pos := dec.position()
			p.sendMsg(PlaybackProgressMsg{
				Position: pos,
				Duration: info.Duration(),
			})
		}
	}
}
