package player

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	tape "github.com/uiscsi/uiscsi-tape"
)

// tapeCmd identifies a command sent to the tape controller.
type tapeCmd int

const (
	tapeCmdRead   tapeCmd = iota // read file into streamBuffer
	tapeCmdSkip                  // fast-forward to next filemark (discard data)
	tapeCmdRewind                // rewind tape to BOT
	tapeCmdStop                  // finish current file, go idle
	tapeCmdClose                 // shut down controller
)

// tapeCmdMsg is a command sent from the player to the tape controller.
type tapeCmdMsg struct {
	cmd   tapeCmd
	sb    *streamBuffer // tapeCmdRead: buffer to write into
	index int           // tapeCmdRead: playlist index
}

// tapeResultKind identifies a result from the tape controller.
type tapeResultKind int

const (
	tapeResultFileComplete tapeResultKind = iota // file read to filemark
	tapeResultPartial                            // file partially read (interrupted)
	tapeResultEOT                                // blank check (double filemark)
	tapeResultSkipped                            // file fast-forwarded past
	tapeResultRewound                            // rewind complete
	tapeResultError                              // unrecoverable error
)

// tapeResult is sent from the tape controller to the player.
type tapeResult struct {
	kind  tapeResultKind
	index int       // file index that was read/skipped
	data  []byte    // file data (nil for skipped/error)
	info  TrackInfo // FLAC metadata (zero for skipped/error)
	err   error
}

// tapeState tracks what the controller is doing.
type tapeState int

const (
	tapeIdle     tapeState = iota // at filemark boundary, waiting for command
	tapeReading                   // reading file into streamBuffer
	tapeSkipping                  // fast-forwarding to next filemark
)

var errStopped = errors.New("tape: stopped by user")

// tapeController is the single goroutine that owns the tape drive.
// All drive operations go through it — no other goroutine may call
// drive.Read or drive.Rewind.
type tapeController struct {
	drive   *tape.Drive
	logger  *slog.Logger
	readBuf int

	cmdCh    chan tapeCmdMsg // commands from player (buffered)
	resultCh chan tapeResult // results to player (buffered)

	// Internal state
	state        tapeState
	filePos      int            // current file number (0-based)
	currentSB    *streamBuffer  // buffer being written to (tapeReading)
	currentIndex int            // playlist index being read
	buf          []byte         // reusable read buffer
	pendingCmd   *tapeCmdMsg    // deferred command (processed after current op)

	// Throttling for tape status messages
	statusCh       chan TapeStatusMsg // optional: for sending status updates
	bytesRead      int64
	readStart      time.Time
	lastStatusTime time.Time

	// Current speed measurement (1-second window)
	windowStart time.Time
	windowBytes int64
	lastCurRate float64 // most recent 1-second rate (reused between windows)
}

func newTapeController(drive *tape.Drive, logger *slog.Logger, readBuf int) *tapeController {
	if readBuf <= 0 {
		readBuf = 262144
	}
	return &tapeController{
		drive:    drive,
		logger:   logger,
		readBuf:  readBuf,
		cmdCh:    make(chan tapeCmdMsg, 4),  // buffered to avoid blocking UI/player
		resultCh: make(chan tapeResult, 16), // must hold rewind + N file results
		buf:      make([]byte, readBuf),
	}
}

// run is the controller's main loop. Call from a dedicated goroutine.
func (tc *tapeController) run(ctx context.Context) {
	defer close(tc.resultCh)
	defer func() {
		if tc.statusCh != nil {
			close(tc.statusCh)
		}
	}()

	for {
		switch tc.state {
		case tapeIdle:
			// Process deferred command before waiting for new ones.
			if tc.pendingCmd != nil {
				cmd := *tc.pendingCmd
				tc.pendingCmd = nil
				tc.handleIdleCmd(ctx, cmd)
				continue
			}
			select {
			case cmd, ok := <-tc.cmdCh:
				if !ok || cmd.cmd == tapeCmdClose {
					return
				}
				tc.handleIdleCmd(ctx, cmd)
			case <-ctx.Done():
				return
			}

		case tapeReading:
			tc.readOneChunk(ctx)

		case tapeSkipping:
			tc.skipOneChunk(ctx)
		}
	}
}

func (tc *tapeController) handleIdleCmd(ctx context.Context, cmd tapeCmdMsg) {
	switch cmd.cmd {
	case tapeCmdRead:
		tc.currentSB = cmd.sb
		tc.currentIndex = cmd.index
		tc.bytesRead = 0
		now := time.Now()
		tc.readStart = now
		tc.windowStart = now
		tc.windowBytes = 0
		tc.lastCurRate = 0
		tc.state = tapeReading
		tc.logger.Debug("tape: reading file", "index", cmd.index, "readBuf", tc.readBuf)

	case tapeCmdSkip:
		// Already at boundary — skip is a no-op.
		tc.logger.Debug("tape: skip (already at boundary)")

	case tapeCmdRewind:
		tc.logger.Debug("tape: rewinding")
		err := tc.drive.Rewind(ctx)
		tc.filePos = 0
		tc.resultCh <- tapeResult{kind: tapeResultRewound, err: err}

	case tapeCmdStop:
		// Already idle, nothing to do.

	case tapeCmdClose:
		// Handled in run() select.
	}
}

func (tc *tapeController) readOneChunk(ctx context.Context) {
	// Check for commands (non-blocking).
	select {
	case cmd := <-tc.cmdCh:
		switch cmd.cmd {
		case tapeCmdSkip:
			tc.logger.Debug("tape: forward mid-read, skipping to filemark",
				"index", tc.currentIndex, "bytesRead", tc.bytesRead)
			tc.currentSB.Complete()
			tc.sendPartialResult()
			tc.state = tapeSkipping
			return
		case tapeCmdStop:
			tc.logger.Debug("tape: stop mid-read, skipping to filemark",
				"index", tc.currentIndex)
			tc.currentSB.Abort(errStopped)
			tc.state = tapeSkipping
			return
		case tapeCmdClose:
			tc.currentSB.Abort(errStopped)
			return
		case tapeCmdRewind:
			// Must skip to boundary first, then rewind.
			tc.currentSB.Abort(errStopped)
			tc.state = tapeSkipping
			tc.pendingCmd = &cmd
			return
		case tapeCmdRead:
			// Read while already reading — defer until current completes.
			tc.logger.Debug("tape: read command while reading, deferring")
			tc.pendingCmd = &cmd
			return
		}
	default:
	}

	n, err := tc.drive.Read(ctx, tc.buf)
	if err != nil {
		if errors.Is(err, tape.ErrFilemark) {
			tc.currentSB.Complete()
			tc.logger.Debug("tape: filemark", "index", tc.currentIndex,
				"bytesRead", tc.bytesRead)
			tc.sendFileResult()
			if tc.statusCh != nil {
				select {
				case tc.statusCh <- TapeStatusMsg{Status: TapeStatus{
					FileNumber:  tc.currentIndex + 1,
					BytesRead:   tc.bytesRead,
					BufferBytes: tc.currentSB.Len(),
					Complete:    true,
				}}:
				default:
				}
			}
			tc.filePos++
			tc.state = tapeIdle
			return
		}
		if errors.Is(err, tape.ErrBlankCheck) {
			tc.currentSB.Complete()
			tc.logger.Debug("tape: blank check (EOT)", "index", tc.currentIndex)
			tc.state = tapeIdle
			tc.resultCh <- tapeResult{kind: tapeResultEOT, index: tc.currentIndex}
			return
		}
		if errors.Is(err, tape.ErrILI) && n > 0 {
			tc.logger.Debug("tape: short record (ILI)", "n", n)
		} else {
			tc.currentSB.Abort(err)
			tc.state = tapeIdle
			tc.resultCh <- tapeResult{kind: tapeResultError, index: tc.currentIndex, err: err}
			return
		}
	}

	if n > 0 {
		tc.currentSB.Write(tc.buf[:n])
		tc.bytesRead += int64(n)

		if tc.statusCh != nil && time.Since(tc.lastStatusTime) >= 200*time.Millisecond {
			now := time.Now()
			tc.lastStatusTime = now

			// Average rate since start of file.
			elapsed := now.Sub(tc.readStart).Seconds()
			avgRate := 0.0
			if elapsed > 0 {
				avgRate = float64(tc.bytesRead) / elapsed / 1e6
			}

			// Current rate over 1-second window. Between window resets,
			// reuse the last computed value so the UI always shows it.
			windowElapsed := now.Sub(tc.windowStart).Seconds()
			if windowElapsed >= 1.0 {
				tc.lastCurRate = float64(tc.bytesRead-tc.windowBytes) / windowElapsed / 1e6
				tc.windowStart = now
				tc.windowBytes = tc.bytesRead
			}

			select {
			case tc.statusCh <- TapeStatusMsg{Status: TapeStatus{
				FileNumber:  tc.currentIndex + 1,
				BytesRead:   tc.bytesRead,
				ReadRate:    avgRate,
				CurrentRate: tc.lastCurRate,
				BufferBytes: tc.currentSB.Len(),
			}}:
			default:
			}
		}
	}
}

func (tc *tapeController) skipOneChunk(ctx context.Context) {
	// Defer commands until skip completes.
	select {
	case cmd := <-tc.cmdCh:
		if cmd.cmd == tapeCmdClose {
			return
		}
		tc.pendingCmd = &cmd
	default:
	}

	// Send seeking status so UI can display progress.
	if tc.statusCh != nil && time.Since(tc.lastStatusTime) >= 200*time.Millisecond {
		tc.lastStatusTime = time.Now()
		select {
		case tc.statusCh <- TapeStatusMsg{Status: TapeStatus{Seeking: true}}:
		default:
		}
	}

	_, err := tc.drive.Read(ctx, tc.buf)
	if err != nil {
		if errors.Is(err, tape.ErrFilemark) {
			tc.logger.Debug("tape: skipped to filemark", "filePos", tc.filePos)
			tc.filePos++
			tc.state = tapeIdle
			tc.resultCh <- tapeResult{kind: tapeResultSkipped, index: tc.currentIndex}
			return
		}
		if errors.Is(err, tape.ErrBlankCheck) {
			tc.logger.Debug("tape: blank check during skip (EOT)")
			tc.state = tapeIdle
			tc.resultCh <- tapeResult{kind: tapeResultEOT, index: tc.currentIndex}
			return
		}
		if errors.Is(err, tape.ErrILI) {
			return // continue skipping
		}
		tc.state = tapeIdle
		tc.resultCh <- tapeResult{
			kind: tapeResultError,
			err:  fmt.Errorf("tape: error during skip: %w", err),
		}
		return
	}
}

// sendFileResult builds and sends a tapeResultFileComplete.
func (tc *tapeController) sendFileResult() {
	sb := tc.currentSB
	if sb == nil || sb.Len() == 0 {
		return
	}
	raw := sb.Bytes()
	dataCopy := make([]byte, len(raw))
	copy(dataCopy, raw)
	info := extractMetadataFromBytes(dataCopy, tc.logger)
	tc.resultCh <- tapeResult{
		kind:  tapeResultFileComplete,
		index: tc.currentIndex,
		data:  dataCopy,
		info:  info,
	}
}

// sendPartialResult sends a tapeResultPartial for an interrupted read.
func (tc *tapeController) sendPartialResult() {
	sb := tc.currentSB
	if sb == nil || sb.Len() == 0 {
		return
	}
	raw := sb.Bytes()
	dataCopy := make([]byte, len(raw))
	copy(dataCopy, raw)
	info := extractMetadataFromBytes(dataCopy, tc.logger)
	tc.resultCh <- tapeResult{
		kind:  tapeResultPartial,
		index: tc.currentIndex,
		data:  dataCopy,
		info:  info,
	}
}

// extractMetadataFromBytes parses FLAC metadata without consuming data.
func extractMetadataFromBytes(data []byte, logger *slog.Logger) TrackInfo {
	if len(data) < 42 {
		return TrackInfo{}
	}
	sb := newStreamBufferFrom(data)
	dec, err := newFlacDecoder(sb, logger)
	if err != nil {
		logger.Debug("metadata: parse failed", "err", err)
		return TrackInfo{}
	}
	return dec.trackInfo()
}
