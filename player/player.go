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
// Navigation is playlist-based: the playlist grows as files are
// discovered from tape, and tracks can be replayed from cache without
// re-reading the tape.
type Player struct {
	drive    *tape.Drive
	logger   *slog.Logger
	prog     *tea.Program
	readBuf  int       // tape read buffer size in bytes
	playlist *Playlist // discovered files with LRU cache

	mu        sync.Mutex
	wg        sync.WaitGroup
	state     State
	cancel    context.CancelFunc
	audioDev  *audioDevice
	streamBuf *streamBuffer
	decoder   *flacDecoder
	startTime time.Time
	bytesRead int64
}

// New creates a Player. readBufSize sets the tape read buffer (0 = 256KB).
// cacheLimit sets the maximum memory for cached tracks (0 = 500MB).
func New(drive *tape.Drive, logger *slog.Logger, readBufSize int, cacheLimit int64) *Player {
	if readBufSize <= 0 {
		readBufSize = 262144
	}
	return &Player{
		drive:    drive,
		logger:   logger,
		readBuf:  readBufSize,
		playlist: NewPlaylist(cacheLimit),
		state:    Stopped,
	}
}

// SetProgram sets the bubbletea program for sending UI messages.
func (p *Player) SetProgram(prog *tea.Program) {
	p.prog = prog
}

// Playlist returns the playlist for UI queries.
func (p *Player) Playlist() *Playlist {
	return p.playlist
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

func (p *Player) sendPlaylistUpdate() {
	entries, current, eot := p.playlist.Snapshot()
	p.sendMsg(PlaylistUpdateMsg{
		Entries: entries,
		Current: current,
		EOT:     eot,
	})
}

// Play starts or resumes playback.
func (p *Player) Play(ctx context.Context) {
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()

	p.logger.Debug("player: Play", "state", state)

	switch state {
	case Stopped:
		cur := p.playlist.Current()
		if cur >= 0 && p.playlist.IsCached(cur) {
			p.playIndex(ctx, cur)
		} else {
			// Play next undiscovered track from tape.
			p.playIndex(ctx, p.playlist.TapeHead())
		}
	case Paused:
		p.resume()
	}
}

// Pause pauses playback.
func (p *Player) Pause() {
	if p.State() == Playing {
		p.setState(Paused)
		if p.audioDev != nil {
			p.audioDev.pause()
		}
	}
}

// TogglePlayPause toggles between play and pause.
func (p *Player) TogglePlayPause(ctx context.Context) {
	p.logger.Debug("player: TogglePlayPause", "state", p.State())

	switch p.State() {
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
	p.wg.Wait()
	p.setState(Stopped)
}

// Forward skips to the next track.
func (p *Player) Forward(ctx context.Context) {
	p.Stop()
	nextIdx := p.playlist.Current() + 1
	if nextIdx < 0 {
		nextIdx = 0
	}
	if p.playlist.IsEOT() && nextIdx >= p.playlist.Len() {
		p.sendMsg(EOTMsg{})
		return
	}
	p.playIndex(ctx, nextIdx)
}

// Back restarts the current track, or goes to the previous track
// if called within the first 3 seconds of playback.
func (p *Player) Back(ctx context.Context) {
	p.mu.Lock()
	elapsed := time.Since(p.startTime)
	p.mu.Unlock()

	p.Stop()

	cur := p.playlist.Current()
	if elapsed < 3*time.Second && cur > 0 {
		p.playIndex(ctx, cur-1)
	} else if cur >= 0 {
		p.playIndex(ctx, cur)
	}
}

// RewindTape rewinds the tape to BOT. Does NOT clear the playlist —
// cached data and metadata are preserved.
func (p *Player) RewindTape(ctx context.Context) {
	p.Stop()
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

// playIndex plays the track at the given playlist index. If the track
// is cached, plays from memory. If it's the next undiscovered track,
// reads from tape. If it's a discovered but evicted track, rewinds
// and re-reads from tape.
func (p *Player) playIndex(ctx context.Context, index int) {
	p.logger.Debug("player: playIndex", "index", index,
		"playlistLen", p.playlist.Len(), "tapeHead", p.playlist.TapeHead())

	trackCtx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	p.cancel = cancel
	p.decoder = nil
	p.streamBuf = nil // release previous track's buffer for GC
	p.bytesRead = 0
	p.startTime = time.Now()
	p.mu.Unlock()

	p.playlist.SetCurrent(index)
	p.sendPlaylistUpdate()

	if p.playlist.IsCached(index) {
		// Play from cache — instant, no copy.
		p.logger.Debug("player: playing from cache", "index", index)
		data := p.playlist.Data(index)
		sb := newStreamBufferFrom(data)

		p.mu.Lock()
		p.streamBuf = sb
		p.mu.Unlock()

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.startDecoder(trackCtx, sb, index)
		}()
		return
	}

	if index == p.playlist.TapeHead() {
		// Read next file from tape.
		p.logger.Debug("player: reading from tape", "index", index)
		sb := newStreamBuffer()

		p.mu.Lock()
		p.streamBuf = sb
		p.mu.Unlock()

		p.setState(Loading)

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.readFromTape(trackCtx, sb, index)
		}()
		return
	}

	if index < p.playlist.Len() {
		// Track was discovered but data evicted. Need to rewind and
		// re-read from tape. This is expensive.
		p.logger.Warn("player: cache miss, rewinding tape", "index", index)
		p.setState(Loading)
		p.sendMsg(ErrorMsg{Err: fmt.Errorf("rewinding tape to re-read track %d (evicted from cache)", index+1)})

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			if err := p.drive.Rewind(trackCtx); err != nil {
				p.sendMsg(ErrorMsg{Err: fmt.Errorf("rewind: %w", err)})
				p.setState(Stopped)
				return
			}
			// Skip past tracks 0..index-1 by reading and discarding.
			for i := range index {
				sb := newStreamBuffer()
				p.readFromTape(trackCtx, sb, i)
				// Data goes to playlist cache via readFromTape → Add.
			}
			// Now read the target track.
			sb := newStreamBuffer()
			p.mu.Lock()
			p.streamBuf = sb
			p.mu.Unlock()
			p.readFromTape(trackCtx, sb, index)
		}()
		return
	}

	// Index beyond what's discovered and not the next tape position.
	// This shouldn't happen in normal operation.
	p.logger.Error("player: invalid index", "index", index,
		"playlistLen", p.playlist.Len(), "tapeHead", p.playlist.TapeHead())
	p.setState(Stopped)
}

func (p *Player) readFromTape(ctx context.Context, sb *streamBuffer, index int) {
	p.logger.Debug("tape: reading file", "index", index, "readBuf", p.readBuf)
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
				p.logger.Debug("tape: filemark", "index", index, "bytesRead", sb.Len())
				sb.Complete()

				// Copy data for playlist — streamBuffer is still used by decoder.
				raw := sb.Bytes()
				dataCopy := make([]byte, len(raw))
				copy(dataCopy, raw)
				info := p.extractMetadata(dataCopy)
				if index < p.playlist.Len() {
					// Re-reading a known file (rewind+skip path) — update cache.
					p.playlist.Recache(index, dataCopy)
				} else {
					// New file discovered from tape.
					p.playlist.Add(dataCopy, info)
				}
				p.sendPlaylistUpdate()

				p.sendMsg(TapeStatusMsg{Status: TapeStatus{
					FileNumber:  index + 1,
					BytesRead:   int64(sb.Len()),
					BufferBytes: sb.Len(),
					Complete:    true,
				}})
				return
			}
			if errors.Is(err, tape.ErrBlankCheck) {
				p.logger.Debug("tape: blank check (EOT)", "index", index)
				sb.Complete()
				p.playlist.MarkEOT()
				p.sendPlaylistUpdate()
				p.sendMsg(EOTMsg{})
				return
			}
			if errors.Is(err, tape.ErrILI) && n > 0 {
				p.logger.Debug("tape: short record (ILI)", "n", n)
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
				FileNumber:  index + 1,
				BytesRead:   br,
				ReadRate:    rate,
				BufferBytes: sb.Len(),
				Complete:    false,
			}})
		}

		// Start decoder once we have data. Launch exactly once.
		p.mu.Lock()
		needStart := p.decoder == nil && sb.Len() > 0
		if needStart {
			p.decoder = &flacDecoder{}
		}
		p.mu.Unlock()
		if needStart {
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.startDecoder(ctx, sb, index)
			}()
		}
	}
}

func (p *Player) startDecoder(ctx context.Context, sb *streamBuffer, index int) {
	p.logger.Debug("decoder: starting", "index", index)

	dec, err := newFlacDecoder(sb, p.logger)
	if err != nil {
		p.logger.Error("decoder: init failed", "err", err)
		p.sendMsg(ErrorMsg{Err: fmt.Errorf("FLAC decode init: %w", err)})
		p.setState(Stopped)
		return
	}

	p.mu.Lock()
	p.decoder = dec
	info := dec.trackInfo()
	p.mu.Unlock()

	// Update playlist metadata.
	p.playlist.UpdateInfo(index, info)
	p.sendMsg(TrackInfoMsg{Info: info})
	p.sendPlaylistUpdate()

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

	// Decode loop.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if p.State() == Paused {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		samples, err := dec.nextFrame()
		if err != nil {
			if err.Error() == "EOF" {
				p.logger.Debug("decoder: track complete", "index", index)
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
				p.logger.Debug("decoder: ring buffer closed, stopping")
				return
			}

			pos := dec.position()
			p.sendMsg(PlaybackProgressMsg{
				Position: pos,
				Duration: info.Duration(),
			})
		}
	}
}

// extractMetadata parses FLAC metadata from raw data without consuming
// the data for playback. Used to populate playlist entries.
func (p *Player) extractMetadata(data []byte) TrackInfo {
	if len(data) < 42 {
		return TrackInfo{}
	}
	sb := newStreamBufferFrom(data)
	dec, err := newFlacDecoder(sb, p.logger)
	if err != nil {
		p.logger.Debug("metadata: parse failed", "err", err)
		return TrackInfo{}
	}
	return dec.trackInfo()
}
