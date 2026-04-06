// Package player implements the tape FLAC player engine.
package player

import (
	"io"
	"sync"
)

// streamBuffer is a growable byte buffer with a blocking io.Reader.
// A writer (tape reader goroutine) appends data via Write. A reader
// (FLAC decoder) reads via Read, blocking when it catches up to the
// write position. When Complete is called (filemark hit), the reader
// receives io.EOF after consuming all data.
//
// After Complete, the full contents are available via Bytes() for
// replay (e.g., "previous track" restarts the decoder from the start
// using bytes.NewReader(buf.Bytes())).
type streamBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	data     []byte
	readPos  int
	complete bool // true after filemark — no more writes expected
	err      error // non-nil if tape read failed
}

// newStreamBuffer creates a ready-to-use streamBuffer.
func newStreamBuffer() *streamBuffer {
	sb := &streamBuffer{}
	sb.cond = sync.NewCond(&sb.mu)
	return sb
}

// Write appends p to the buffer and wakes any blocked reader.
// Safe to call from a different goroutine than Read.
func (sb *streamBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.data = append(sb.data, p...)
	sb.cond.Broadcast()
	return len(p), nil
}

// Read reads from the buffer, blocking if the reader has caught up to
// the writer. Returns io.EOF when Complete has been called and all data
// is consumed. Returns the error from Abort if set.
func (sb *streamBuffer) Read(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for sb.readPos >= len(sb.data) {
		// No unread data available.
		if sb.err != nil {
			return 0, sb.err
		}
		if sb.complete {
			return 0, io.EOF
		}
		// Block until writer adds data, completes, or aborts.
		sb.cond.Wait()
	}

	n := copy(p, sb.data[sb.readPos:])
	sb.readPos += n
	return n, nil
}

// Complete marks the buffer as finished (filemark hit). After this,
// Read will return io.EOF once all buffered data is consumed.
// No more Write calls should be made after Complete.
func (sb *streamBuffer) Complete() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.complete = true
	sb.cond.Broadcast()
}

// Abort marks the buffer with an error (e.g., tape read failure).
// Any blocked or future Read will return this error.
func (sb *streamBuffer) Abort(err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.err = err
	sb.cond.Broadcast()
}

// Bytes returns the full buffered contents. Only meaningful after
// Complete has been called — used for track replay.
func (sb *streamBuffer) Bytes() []byte {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.data
}

// Len returns the current number of bytes written to the buffer.
func (sb *streamBuffer) Len() int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return len(sb.data)
}

// IsComplete reports whether the buffer has been marked complete.
func (sb *streamBuffer) IsComplete() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.complete
}
