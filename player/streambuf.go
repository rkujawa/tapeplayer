// Package player implements the tape FLAC player engine.
package player

import (
	"io"
	"sync"
	"sync/atomic"
)

// streamBuffer accumulates tape data and provides a non-contending
// io.Reader for the FLAC decoder.
//
// Design: the tape writer calls Write, which appends to an internal
// slice under a write lock and updates an atomic write position. The FLAC
// decoder calls Read, which snapshots the slice header under a read lock
// then copies from the immutable portion of the backing array.
//
// Growth policy: starts at 4 MB, doubles until 64 MB, then grows in
// fixed 64 MB chunks. No hard cap — single FLAC files on LTO tapes
// can exceed 512 MB.
//
// Concurrency: sync.RWMutex protects the slice header. Write holds
// the exclusive lock; Read and Bytes hold the shared read lock. The
// atomic writePos allows readers to check data availability without
// any lock.

// wrappedError is a consistent concrete type for atomic.Value storage.
// atomic.Value panics if different concrete error types are stored;
// wrapping in a struct avoids this.
type wrappedError struct{ err error }

type streamBuffer struct {
	mu       sync.RWMutex
	data     []byte
	writePos atomic.Int64 // bytes written so far (atomic, no lock for reader)
	readPos  int
	complete atomic.Bool
	err      atomic.Value // stores *wrappedError

	// For blocking when reader catches up to writer.
	notify chan struct{}
}

const (
	initialStreamBufCap = 4 * 1024 * 1024  // 4 MB initial allocation
	growthThreshold     = 64 * 1024 * 1024  // double until 64 MB
	fixedGrowthChunk    = 64 * 1024 * 1024  // then 64 MB fixed chunks
)

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

// growIfNeeded ensures the backing array can hold n more bytes.
// Caller must hold the exclusive write lock.
func (sb *streamBuffer) growIfNeeded(n int) {
	needed := len(sb.data) + n
	if needed <= cap(sb.data) {
		return
	}
	newCap := cap(sb.data)
	if newCap == 0 {
		newCap = initialStreamBufCap
	}
	for newCap < needed {
		if newCap < growthThreshold {
			newCap *= 2
		} else {
			newCap += fixedGrowthChunk
		}
	}
	newData := make([]byte, len(sb.data), newCap)
	copy(newData, sb.data)
	sb.data = newData
}

// Write appends p to the buffer. Takes the exclusive lock for append
// and growth, then updates atomic writePos and signals the reader.
func (sb *streamBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	sb.growIfNeeded(len(p))
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
// Takes a read lock to snapshot the slice header, then copies from
// the immutable portion of the backing array.
// Blocks only when the reader has caught up to the writer.
func (sb *streamBuffer) Read(p []byte) (int, error) {
	for {
		wp := int(sb.writePos.Load())

		if sb.readPos < wp {
			// Data available — snapshot slice header under read lock.
			sb.mu.RLock()
			src := sb.data[sb.readPos:]
			sb.mu.RUnlock()

			n := copy(p, src)
			sb.readPos += n
			return n, nil
		}

		// Caught up to writer — check for completion.
		if v := sb.err.Load(); v != nil {
			return 0, v.(*wrappedError).err
		}
		if sb.complete.Load() {
			return 0, io.EOF
		}

		// Wait for more data (or completion signal).
		<-sb.notify
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
	sb.err.Store(&wrappedError{err: err})
	select {
	case sb.notify <- struct{}{}:
	default:
	}
}

// Bytes returns the buffered contents. Safe to call at any time,
// including before Complete — returns whatever has been written so far.
func (sb *streamBuffer) Bytes() []byte {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
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
