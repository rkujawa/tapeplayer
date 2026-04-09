// Package player implements the tape FLAC player engine.
package player

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// streamBuffer accumulates tape data and provides a non-contending
// io.Reader for the FLAC decoder.
//
// Design: the tape writer calls Write, which appends to an internal
// slice under a mutex and updates an atomic write position. The FLAC
// decoder calls Read, which reads up to the current atomic write
// position WITHOUT taking the mutex — it just reads from the portion
// of the slice that is already written and immutable.
//
// This works because Go slice backing arrays are stable: once bytes
// are written at positions 0..N, those bytes never move (append only
// grows the slice, it doesn't modify existing bytes). The reader only
// accesses positions 0..writePos, which are frozen.
//
// The only contention is when append triggers a reallocation (copy to
// new backing array). Pre-allocating 64MB makes this rare. When it
// happens, the reader briefly sees stale data from the old backing
// array — but since readPos < old writePos, those bytes are identical
// in both arrays.
type streamBuffer struct {
	mu       sync.Mutex
	data     []byte
	writePos atomic.Int64 // bytes written so far (atomic, no lock for reader)
	readPos  int
	complete atomic.Bool
	err      atomic.Value // stores error

	// For blocking when reader catches up to writer.
	notify chan struct{}
}

// Pre-allocate 512MB. This avoids reallocations during tape reading
// (which hold the mutex and block the decoder). 512MB covers any
// single FLAC file. The memory is virtual until touched — the OS
// only allocates physical pages as data is written.
const initialStreamBufCap = 512 * 1024 * 1024

// newStreamBufferFrom creates a pre-filled, completed streamBuffer.
func newStreamBufferFrom(data []byte) *streamBuffer {
	sb := &streamBuffer{
		data:   data,
		notify: make(chan struct{}, 1),
	}
	sb.writePos.Store(int64(len(data)))
	sb.complete.Store(true)
	return sb
}

// newStreamBuffer creates a ready-to-use streamBuffer.
func newStreamBuffer() *streamBuffer {
	return &streamBuffer{
		data:   make([]byte, 0, initialStreamBufCap),
		notify: make(chan struct{}, 1),
	}
}

// Write appends p to the buffer. Takes mutex briefly for append,
// then updates atomic writePos and signals the reader.
func (sb *streamBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	sb.data = append(sb.data, p...)
	newLen := int64(len(sb.data))
	sb.mu.Unlock()

	sb.writePos.Store(newLen)

	// Non-blocking signal to wake reader if it's waiting.
	select {
	case sb.notify <- struct{}{}:
	default:
	}

	return len(p), nil
}

// Read reads from the buffer up to the current write position.
// Does NOT take the mutex — reads from the immutable portion of the
// backing array (positions 0..writePos are frozen after write).
// Blocks only when the reader has caught up to the writer.
func (sb *streamBuffer) Read(p []byte) (int, error) {
	for {
		wp := int(sb.writePos.Load())

		if sb.readPos < wp {
			// Data available — read without any lock.
			// Safe because data[0..wp-1] is immutable (only append grows).
			// We must read data slice header under lock in case append
			// reallocated the backing array.
			sb.mu.Lock()
			src := sb.data[sb.readPos:]
			sb.mu.Unlock()

			n := copy(p, src)
			sb.readPos += n
			return n, nil
		}

		// Caught up to writer — check for completion.
		if sb.err.Load() != nil {
			return 0, sb.err.Load().(error)
		}
		if sb.complete.Load() {
			return 0, io.EOF
		}

		// Wait for more data (or completion signal).
		select {
		case <-sb.notify:
			// New data or completion — retry.
		case <-time.After(10 * time.Millisecond):
			// Periodic check in case we missed a signal.
		}
	}
}

// Complete marks the buffer as finished.
func (sb *streamBuffer) Complete() {
	sb.complete.Store(true)
	select {
	case sb.notify <- struct{}{}:
	default:
	}
}

// Abort marks the buffer with an error.
func (sb *streamBuffer) Abort(err error) {
	sb.err.Store(err)
	select {
	case sb.notify <- struct{}{}:
	default:
	}
}

// Bytes returns the full buffered contents for track replay.
// Must only be called after Complete.
func (sb *streamBuffer) Bytes() []byte {
	if !sb.complete.Load() {
		panic("streamBuffer.Bytes() called before Complete()")
	}
	return sb.data
}

// ResetReader resets the read position to the beginning of the buffer.
// The caller must ensure no concurrent Read calls are in progress.
func (sb *streamBuffer) ResetReader() {
	sb.readPos = 0
}

// Len returns the current number of bytes written.
func (sb *streamBuffer) Len() int {
	return int(sb.writePos.Load())
}

// IsComplete reports whether the buffer has been marked complete.
func (sb *streamBuffer) IsComplete() bool {
	return sb.complete.Load()
}
