// Package player implements the tape FLAC player engine.
package player

import (
	"io"
	"sync"
)

// streamBuffer provides two independent interfaces for tape data:
//
//  1. An io.Reader (via Read) backed by a channel of chunks — lock-free
//     for the hot path between tape reader and FLAC decoder.
//  2. A Bytes() method that returns accumulated data for playlist cache
//     after the file is complete.
//
// The tape writer calls Write (sends chunks to channel + accumulates).
// The FLAC decoder calls Read (receives from channel). These never
// contend on a mutex. Bytes() is only called after Complete, when the
// writer is done.
type streamBuffer struct {
	ch       chan []byte    // chunks from writer to reader (hot path)
	current  []byte        // partially consumed chunk from last Read
	mu       sync.Mutex    // protects accum, complete, err — NOT the hot path
	accum    []byte        // accumulated data for Bytes() / cache
	readPos  int           // not used in channel mode
	complete bool
	err      error
	closed   bool
}

const initialStreamBufCap = 64 * 1024 * 1024

// newStreamBufferFrom creates a pre-filled, completed streamBuffer
// wrapping existing data without copying. The caller must not modify
// data after this call. Uses a pre-filled channel for Read.
func newStreamBufferFrom(data []byte) *streamBuffer {
	sb := &streamBuffer{
		ch:       make(chan []byte, 1),
		accum:    data,
		complete: true,
	}
	// Put all data as one chunk and close.
	sb.ch <- data
	close(sb.ch)
	return sb
}

// newStreamBuffer creates a ready-to-use streamBuffer.
func newStreamBuffer() *streamBuffer {
	return &streamBuffer{
		ch:    make(chan []byte, 16), // buffer 16 chunks ahead
		accum: make([]byte, 0, initialStreamBufCap),
	}
}

// Write sends a chunk to the reader channel and accumulates for cache.
// Safe to call from a different goroutine than Read — they never share
// a mutex on the hot path.
func (sb *streamBuffer) Write(p []byte) (int, error) {
	// Make a copy — the caller may reuse the buffer.
	chunk := make([]byte, len(p))
	copy(chunk, p)

	// Send to reader (non-blocking if channel has space).
	sb.ch <- chunk

	// Accumulate for Bytes() / playlist cache.
	sb.mu.Lock()
	sb.accum = append(sb.accum, chunk...)
	sb.mu.Unlock()

	return len(p), nil
}

// Read implements io.Reader by consuming chunks from the channel.
// Blocks if no data is available yet. Returns io.EOF when Complete
// has been called and all chunks are consumed.
func (sb *streamBuffer) Read(p []byte) (int, error) {
	// Drain leftover from previous chunk.
	if len(sb.current) > 0 {
		n := copy(p, sb.current)
		sb.current = sb.current[n:]
		return n, nil
	}

	// Receive next chunk from channel.
	chunk, ok := <-sb.ch
	if !ok {
		// Channel closed — check for error or EOF.
		sb.mu.Lock()
		err := sb.err
		sb.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return 0, io.EOF
	}

	n := copy(p, chunk)
	if n < len(chunk) {
		sb.current = chunk[n:]
	}
	return n, nil
}

// Complete marks the buffer as finished (filemark hit). Closes the
// channel so Read returns io.EOF after consuming remaining chunks.
func (sb *streamBuffer) Complete() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.closed {
		return
	}
	sb.complete = true
	sb.closed = true
	close(sb.ch)
}

// Abort marks the buffer with an error. Closes the channel so Read
// returns the error after consuming remaining chunks.
func (sb *streamBuffer) Abort(err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.closed {
		return
	}
	sb.err = err
	sb.closed = true
	close(sb.ch)
}

// Bytes returns the accumulated data for playlist cache.
// Must only be called after Complete — panics otherwise.
func (sb *streamBuffer) Bytes() []byte {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if !sb.complete {
		panic("streamBuffer.Bytes() called before Complete()")
	}
	return sb.accum
}

// Len returns the current accumulated data size.
func (sb *streamBuffer) Len() int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return len(sb.accum)
}

// IsComplete reports whether the buffer has been marked complete.
func (sb *streamBuffer) IsComplete() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.complete
}
