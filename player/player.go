package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"time"

	tape "github.com/uiscsi/uiscsi-tape"

	tea "github.com/charmbracelet/bubbletea"
)

// Player coordinates tape reading, FLAC decoding, and audio playback.
// Navigation is playlist-based: the playlist grows as files are
// discovered from tape, and tracks can be replayed from cache without
// re-reading the tape.
//
// Tape I/O is owned exclusively by a tapeController goroutine.
// The player sends commands and receives results — it never calls
// drive.Read or drive.Rewind directly.
type Player struct {
	logger   *slog.Logger
	prog     *tea.Program
	playlist *Playlist       // discovered files with LRU cache
	msgCh    chan tea.Msg     // buffered UI message channel (single drainer)
	tc       *tapeController // exclusive owner of the tape drive

	mu           sync.Mutex
	wg           sync.WaitGroup
	bgWg         sync.WaitGroup // tracks background drainer goroutines
	state        State
	trackCancel  context.CancelFunc // cancels decoder/audio only, not tape
	audioDev     *audioDevice
	streamBuf    *streamBuffer
	decoder      *flacDecoder
	startTime    time.Time
	lastProgress time.Time // throttle PlaybackProgressMsg
	closeOnce    sync.Once

	// newAudioFunc creates an audio device. Defaults to newAudioDevice.
	// Tests override this to inject audio failures without C bindings.
	newAudioFunc func(sampleRate uint32, channels uint8, bitsPerSample uint8, logger *slog.Logger) (*audioDevice, error)
}

// New creates a Player. readBufSize sets the tape read buffer (0 = 256KB).
// cacheLimit sets the maximum memory for cached tracks (0 = 500MB).
func New(drive *tape.Drive, logger *slog.Logger, readBufSize int, cacheLimit int64) *Player {
	if readBufSize <= 0 {
		readBufSize = 262144
	}
	return &Player{
		logger:       logger,
		playlist:     NewPlaylist(cacheLimit),
		msgCh:        make(chan tea.Msg, 64),
		tc:           newTapeController(drive, logger, readBufSize),
		state:        Stopped,
		newAudioFunc: newAudioDevice,
	}
}

// SetProgram sets the bubbletea program for sending UI messages.
// Starts the message drainer, tape controller, and result listener.
func (p *Player) SetProgram(prog *tea.Program) {
	p.prog = prog
	go func() {
		for msg := range p.msgCh {
			prog.Send(msg)
		}
	}()
	// Wire tape status messages to the UI.
	p.tc.statusCh = make(chan TapeStatusMsg, 4)
	p.bgWg.Add(1)
	go func() {
		defer p.bgWg.Done()
		for msg := range p.tc.statusCh {
			p.sendMsg(msg)
		}
	}()
	go p.tc.run(context.Background())
	p.bgWg.Add(1)
	go func() {
		defer p.bgWg.Done()
		p.handleTapeResults()
	}()
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
	// State transitions are infrequent and must not be dropped.
	p.msgCh <- StateChangedMsg{State: s}
}

// sendMsg sends a best-effort UI message. Dropped if the channel is full.
func (p *Player) sendMsg(msg tea.Msg) {
	select {
	case p.msgCh <- msg:
	default:
	}
}

// sendMsgBlocking sends a lifecycle-critical message that must not be
// dropped. Blocks until delivered or context is cancelled.
func (p *Player) sendMsgBlocking(ctx context.Context, msg tea.Msg) {
	select {
	case p.msgCh <- msg:
	case <-ctx.Done():
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
			p.playFromCache(ctx, cur)
		} else {
			p.playFromTape(ctx, p.playlist.TapeHead())
		}
	case Paused:
		p.resume()
	default:
		// Loading or Playing: no action
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
	default:
		// Loading: no action while loading
	}
}

// Stop stops decoder and audio. Tells the tape controller to finish
// the current file (skip to filemark boundary) so tape position stays known.
func (p *Player) Stop() {
	p.stopDecoder()
	go func() { p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdStop} }()
	p.setState(Stopped)
}

// Forward skips to the next track.
func (p *Player) Forward(ctx context.Context) {
	p.stopDecoder()

	nextIdx := p.playlist.Current() + 1
	if nextIdx < 0 {
		nextIdx = 0
	}
	if p.playlist.IsEOT() && nextIdx >= p.playlist.Len() {
		p.sendMsgBlocking(ctx, EOTMsg{})
		return
	}
	if p.playlist.IsCached(nextIdx) {
		p.playFromCache(ctx, nextIdx)
		return
	}
	// Skip + read must not block the UI thread. The tape controller
	// processes commands sequentially: skip finishes to filemark, then
	// read starts. This can take tens of seconds on DDS drives.
	p.setState(Loading)
	p.playlist.SetCurrent(nextIdx)
	p.sendPlaylistUpdate()
	go p.forwardFromTape(ctx, nextIdx)
}

// forwardFromTape sends skip + read commands to the tape controller.
// Runs in its own goroutine to avoid blocking the bubbletea event loop.
func (p *Player) forwardFromTape(ctx context.Context, index int) {
	p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdSkip}
	p.playFromTape(ctx, index)
}

// Back restarts the current track, or goes to the previous track
// if called within the first 3 seconds of playback.
func (p *Player) Back(ctx context.Context) {
	p.mu.Lock()
	elapsed := time.Since(p.startTime)
	p.mu.Unlock()

	// Grab the live streamBuffer before stopping the decoder — if we're
	// restarting the current track mid-load, we can reuse it.
	p.mu.Lock()
	liveSB := p.streamBuf
	p.mu.Unlock()

	p.stopDecoder()

	cur := p.playlist.Current()
	if elapsed < 3*time.Second && cur > 0 {
		cur--
	}
	if cur < 0 {
		return // no track has been played yet
	}
	switch {
	case p.playlist.IsCached(cur):
		go func() { p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdStop} }()
		p.playFromCache(ctx, cur)
	case cur == p.playlist.Current() && liveSB != nil && liveSB.Len() > 0:
		// Restarting current track while tape is still loading. The
		// streamBuffer has all data from the beginning — reset its
		// read position and start a new decoder. The tape controller
		// keeps reading into the same buffer undisturbed.
		p.logger.Debug("player: restarting decoder from live buffer", "index", cur)
		liveSB.ResetReader()

		trackCtx, cancel := context.WithCancel(ctx)
		p.mu.Lock()
		p.trackCancel = cancel
		p.decoder = nil
		p.streamBuf = liveSB
		p.startTime = time.Now()
		p.lastProgress = time.Time{} // force immediate progress update
		p.mu.Unlock()

		p.playlist.SetCurrent(cur)
		p.sendPlaylistUpdate()

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.startDecoder(trackCtx, liveSB, cur)
		}()
	default:
		// Track was evicted — need to rewind and re-read.
		go p.rewindAndPlay(ctx, cur)
	}
}

// RewindTape rewinds the tape to BOT. Does NOT clear the playlist —
// cached data and metadata are preserved.
func (p *Player) RewindTape(_ context.Context) {
	p.stopDecoder()
	go func() {
		p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdStop}
		p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRewind}
	}()
	p.setState(Stopped)
}

// Close releases audio resources and shuts down the tape controller.
func (p *Player) Close() {
	p.mu.Lock()
	if p.trackCancel != nil {
		p.trackCancel()
		p.trackCancel = nil
	}
	p.state = Stopped
	p.mu.Unlock()

	if p.audioDev != nil {
		p.audioDev.stop()
		p.audioDev.close()
		p.audioDev = nil
	}
	p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdClose}
	// Wait for tape controller drainers (statusCh, handleTapeResults) to
	// exit before closing msgCh — they may still be sending.
	p.bgWg.Wait()
	p.closeOnce.Do(func() { close(p.msgCh) })
}

func (p *Player) resume() {
	p.setState(Playing)
	if p.audioDev != nil {
		p.audioDev.resume()
	}
}

// stopDecoder cancels the decoder/audio goroutines and waits for them.
// Does NOT touch the tape — the controller handles tape position.
func (p *Player) stopDecoder() {
	p.mu.Lock()
	if p.trackCancel != nil {
		p.trackCancel()
		p.trackCancel = nil
	}
	p.mu.Unlock()

	if p.audioDev != nil {
		p.audioDev.stop()
	}
	p.wg.Wait()
}

// playFromCache starts playback from cached data. No tape involvement.
func (p *Player) playFromCache(ctx context.Context, index int) {
	p.logger.Debug("player: playing from cache", "index", index)
	trackCtx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	p.trackCancel = cancel
	p.decoder = nil
	p.streamBuf = nil
	p.startTime = time.Now()
	p.mu.Unlock()

	p.playlist.SetCurrent(index)
	p.sendPlaylistUpdate()

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
}

// playFromTape sends a read command to the tape controller and starts
// the decoder once data arrives.
func (p *Player) playFromTape(ctx context.Context, index int) {
	p.logger.Debug("player: reading from tape", "index", index)
	trackCtx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	p.trackCancel = cancel
	p.decoder = nil
	p.streamBuf = nil
	p.startTime = time.Now()
	p.mu.Unlock()

	p.playlist.SetCurrent(index)
	p.sendPlaylistUpdate()
	p.setState(Loading)

	sb := newStreamBuffer()
	p.mu.Lock()
	p.streamBuf = sb
	p.mu.Unlock()

	// Tell the tape controller to read into this buffer.
	// Send from a goroutine if the controller might be busy (e.g., previous
	// stop still skipping to filemark). The decoder-wait goroutine below
	// handles the delay.
	go func() { p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRead, sb: sb, index: index} }()

	// Start decoder once data arrives in the streamBuffer.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		// Wait for first data or completion.
		for sb.Len() == 0 && !sb.IsComplete() {
			select {
			case <-trackCtx.Done():
				return
			case <-sb.notify:
			}
		}
		if sb.Len() > 0 {
			p.startDecoder(trackCtx, sb, index)
		}
	}()
}

// rewindAndPlay rewinds the tape and skips to the target index.
func (p *Player) rewindAndPlay(ctx context.Context, index int) {
	p.logger.Warn("player: cache miss, rewinding tape", "index", index)
	p.setState(Loading)
	p.sendMsgBlocking(ctx, ErrorMsg{Err: fmt.Errorf("rewinding tape to re-read track %d (evicted from cache)", index+1)})

	trackCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.trackCancel = cancel
	p.startTime = time.Now()
	p.mu.Unlock()

	p.playlist.SetCurrent(index)
	p.sendPlaylistUpdate()

	// Rewind, skip past earlier files, then read the target.
	p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRewind}
	for i := range index {
		// Read each file (data goes to playlist via handleTapeResults).
		sb := newStreamBuffer()
		p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRead, sb: sb, index: i}
	}

	// Now read the target file with a live streamBuffer.
	sb := newStreamBuffer()
	p.mu.Lock()
	p.streamBuf = sb
	p.mu.Unlock()

	p.tc.cmdCh <- tapeCmdMsg{cmd: tapeCmdRead, sb: sb, index: index}

	// Start decoder once data arrives.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for sb.Len() == 0 && !sb.IsComplete() {
			select {
			case <-trackCtx.Done():
				return
			case <-sb.notify:
			}
		}
		if sb.Len() > 0 {
			p.startDecoder(trackCtx, sb, index)
		}
	}()
}

// handleTapeResults processes results from the tape controller.
// Runs as a dedicated goroutine for the lifetime of the player.
func (p *Player) handleTapeResults() {
	for result := range p.tc.resultCh {
		switch result.kind {
		case tapeResultFileComplete:
			if result.data != nil {
				if result.index < p.playlist.Len() {
					p.playlist.Recache(result.index, result.data)
				} else {
					p.playlist.Add(result.data, result.info)
				}
			}
			p.sendPlaylistUpdate()

		case tapeResultPartial:
			// File partially read (interrupted by Forward/Stop). Cache
			// the partial data for decoder use but mark incomplete.
			if result.data != nil {
				if result.index < p.playlist.Len() {
					// Already known — update with partial data.
					p.playlist.Recache(result.index, result.data)
				} else {
					p.playlist.AddPartial(result.data, result.info)
				}
			}
			p.sendPlaylistUpdate()

		case tapeResultSkipped:
			// File was fast-forwarded past — add placeholder to advance tapeHead.
			if result.index >= p.playlist.Len() {
				p.playlist.Add(nil, TrackInfo{})
			}
			p.sendPlaylistUpdate()

		case tapeResultEOT:
			p.playlist.MarkEOT()
			// Reset current to last valid track — Forward() speculatively
			// set current to the EOT index before the read completed.
			if last := p.playlist.Len() - 1; last >= 0 {
				p.playlist.SetCurrent(last)
			}
			p.sendPlaylistUpdate()
			p.sendMsgBlocking(context.Background(), EOTMsg{})

		case tapeResultRewound:
			if result.err != nil {
				p.sendMsgBlocking(context.Background(), ErrorMsg{
					Err: fmt.Errorf("rewind: %w", result.err),
				})
			}

		case tapeResultError:
			p.sendMsgBlocking(context.Background(), ErrorMsg{Err: result.err})
		}
	}
}

func (p *Player) startDecoder(ctx context.Context, sb *streamBuffer, index int) {
	p.logger.Debug("decoder: starting", "index", index)

	dec, err := newFlacDecoder(sb, p.logger)
	if err != nil {
		p.logger.Error("decoder: init failed", "err", err)
		p.sendMsgBlocking(ctx, ErrorMsg{Err: fmt.Errorf("FLAC decode init: %w", err)})
		p.setState(Stopped)
		return
	}

	p.mu.Lock()
	p.decoder = dec
	info := dec.trackInfo()
	p.mu.Unlock()

	// Update playlist metadata.
	p.playlist.UpdateInfo(index, info)
	p.sendMsgBlocking(ctx, TrackInfoMsg{Info: info})
	p.sendPlaylistUpdate()

	// Initialize audio device if needed.
	if p.audioDev == nil {
		ad, err := p.newAudioFunc(info.SampleRate, info.Channels, info.BitsPerSample, p.logger)
		if err != nil {
			p.sendMsgBlocking(ctx, AudioErrorMsg{Err: fmt.Errorf("audio init: %w", err)})
			p.setState(Stopped)
			return
		}
		p.audioDev = ad
		ai := ad.audioInfo()
		p.logger.Debug("audio: device initialized",
			"device", ai.DeviceName,
			"sampleRate", ai.SampleRate,
			"format", ai.Format,
			"channels", ai.Channels)
		p.sendMsg(AudioInfoMsg{Info: ai})
	}

	p.audioDev.reset()
	p.setState(Playing)
	if err := p.audioDev.start(); err != nil {
		p.logger.Error("audio: start failed", "err", err)
		p.audioDev.close() // Prevent malgo context leak (T-06-03).
		p.audioDev = nil
		p.sendMsgBlocking(ctx, AudioErrorMsg{Err: fmt.Errorf("audio start: %w", err)})
		p.setState(Stopped)
		return
	}

	// Decode loop with instrumentation.
	var (
		frames        int
		totalDecode   time.Duration
		totalWrite    time.Duration
		maxDecode     time.Duration
		maxWrite      time.Duration
		lastDiag      = time.Now()
		prevUnderruns int
	)

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

		t0 := time.Now()
		samples, err := dec.nextFrame()
		decodeTime := time.Since(t0)

		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if errors.Is(err, io.ErrUnexpectedEOF) && !dec.isComplete() {
					p.logger.Warn("decoder: unexpected EOF before all samples decoded",
						"decoded", dec.samples, "expected", dec.totalSamples(),
						"index", index)
				}
				p.logger.Debug("decoder: track complete", "index", index,
					"frames", frames,
					"underruns", p.audioDev.ring.Underruns())
				for p.audioDev.ring.Available() > 0 {
					time.Sleep(10 * time.Millisecond)
				}
				p.sendMsgBlocking(ctx, TrackEndMsg{})
				return
			}
			// Tape uses fixed-size blocks; the last block of a FLAC file
			// contains padding bytes past the actual stream data. If all
			// expected samples have been decoded, treat any frame parse
			// error (e.g., invalid sync-code) as normal end-of-track.
			if dec.isComplete() {
				p.logger.Debug("decoder: track complete (trailing data after last frame)", "index", index,
					"frames", frames,
					"underruns", p.audioDev.ring.Underruns())
				for p.audioDev.ring.Available() > 0 {
					time.Sleep(10 * time.Millisecond)
				}
				p.sendMsgBlocking(ctx, TrackEndMsg{})
				return
			}
			p.logger.Error("decoder: frame error", "err", err)
			p.sendMsgBlocking(ctx, ErrorMsg{Err: fmt.Errorf("FLAC decode: %w", err)})
			p.setState(Stopped)
			return
		}

		if len(samples) > 0 {
			t1 := time.Now()
			if _, err := p.audioDev.ring.Write(samples); err != nil {
				p.logger.Debug("decoder: ring buffer closed, stopping")
				return
			}
			writeTime := time.Since(t1)

			frames++
			totalDecode += decodeTime
			totalWrite += writeTime
			if decodeTime > maxDecode {
				maxDecode = decodeTime
			}
			if writeTime > maxWrite {
				maxWrite = writeTime
			}

			// Periodic diagnostics every 2 seconds.
			if time.Since(lastDiag) >= 2*time.Second {
				underruns := p.audioDev.ring.Underruns()
				newUnderruns := underruns - prevUnderruns
				prevUnderruns = underruns

				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)

				p.logger.Debug("decoder: diag",
					"frames", frames,
					"avgDecode", totalDecode/time.Duration(frames),
					"maxDecode", maxDecode,
					"avgWrite", totalWrite/time.Duration(frames),
					"maxWrite", maxWrite,
					"ringAvail", p.audioDev.ring.Available(),
					"ringSize", ringSize(p.audioDev),
					"underruns", newUnderruns,
					"totalUnderruns", underruns,
					"heapMB", ms.HeapAlloc/1024/1024,
					"gcPauses", ms.NumGC,
					"lastGCPause", ms.PauseNs[(ms.NumGC+255)%256],
				)

				lastDiag = time.Now()
				maxDecode = 0
				maxWrite = 0
			}

			// Throttle progress updates to every 200ms.
			// Playback position = decoded position minus ring buffer backlog.
			// The decoder runs ahead of audio output by up to ~10s (ring size).
			p.mu.Lock()
			progressDue := time.Since(p.lastProgress) >= 200*time.Millisecond
			if progressDue {
				p.lastProgress = time.Now()
			}
			p.mu.Unlock()
			if progressDue {
				// Match decoder's bytes-per-sample logic.
				var bps int
				switch {
				case info.BitsPerSample <= 16:
					bps = 2
				case info.BitsPerSample <= 24:
					bps = 3
				default:
					bps = 4
				}
				bytesPerFrame := int(info.Channels) * bps
				ringBacklog := time.Duration(0)
				if bytesPerFrame > 0 && info.SampleRate > 0 {
					ringFrames := p.audioDev.ring.Available() / bytesPerFrame
					ringBacklog = time.Duration(ringFrames) * time.Second / time.Duration(info.SampleRate)
				}
				pos := dec.position() - ringBacklog
				if pos < 0 {
					pos = 0
				}
				p.sendMsg(PlaybackProgressMsg{
					Position: pos,
					Duration: info.Duration(),
				})
			}
		}
	}
}
