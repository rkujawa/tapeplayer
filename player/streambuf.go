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
// Pre-allocated to 64MB. With pre-allocation, append is just a memcpy
// (~50µs for 512KB at memory bandwidth). The mutex is held only during
// this memcpy — short enough that the decoder's reads rarely contend.
//
// After Complete, Bytes() returns the full contents for playlist cache.
type streamBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	data     []byte
	readPos  int
	complete bool
	err      error
}

const initialStreamBufCap = 64 * 1024 * 1024

// newStreamBufferFrom creates a pre-filled, completed streamBuffer
// wrapping existing data without copying.
func newStreamBufferFrom(data []byte) *streamBuffer {
	sb := &streamBuffer{
		data:     data,
		complete: true,
	}
	sb.cond = sync.NewCond(&sb.mu)
	return sb
}

// newStreamBuffer creates a ready-to-use streamBuffer.
func newStreamBuffer() *streamBuffer {
	sb := &streamBuffer{
		data: make([]byte, 0, initialStreamBufCap),
	}
	sb.cond = sync.NewCond(&sb.mu)
	return sb
}

// Write appends p to the buffer and wakes any blocked reader.
func (sb *streamBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	sb.data = append(sb.data, p...)
	sb.mu.Unlock()
	sb.cond.Broadcast()
	return len(p), nil
}

// Read reads from the buffer, blocking if the reader has caught up.
func (sb *streamBuffer) Read(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for sb.readPos >= len(sb.data) {
		if sb.err != nil {
			return 0, sb.err
		}
		if sb.complete {
			return 0, io.EOF
		}
		sb.cond.Wait()
	}

	n := copy(p, sb.data[sb.readPos:])
	sb.readPos += n
	return n, nil
}

// Complete marks the buffer as finished.
func (sb *streamBuffer) Complete() {
	sb.mu.Lock()
	sb.complete = true
	sb.mu.Unlock()
	sb.cond.Broadcast()
}

// Abort marks the buffer with an error.
func (sb *streamBuffer) Abort(err error) {
	sb.mu.Lock()
	sb.err = err
	sb.mu.Unlock()
	sb.cond.Broadcast()
}

// Bytes returns the full buffered contents for track replay.
// Must only be called after Complete.
func (sb *streamBuffer) Bytes() []byte {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if !sb.complete {
		panic("streamBuffer.Bytes() called before Complete()")
	}
	return sb.data
}

// Len returns the current number of bytes written.
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
