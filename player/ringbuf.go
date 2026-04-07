package player

import (
	"io"
	"sync"
)

// ringBuffer is a fixed-size circular buffer for PCM audio samples.
// The decoder goroutine writes decoded samples via Write, and the malgo
// audio callback reads them via Read. Coordination uses a mutex +
// condition variable instead of spin-waiting.
type ringBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	size   int
	rpos   int  // read position
	wpos   int  // write position
	used   int  // bytes currently buffered
	closed bool // true after Close — Write returns immediately
}

// newRingBuffer creates a ring buffer with the given capacity in bytes.
func newRingBuffer(size int) *ringBuffer {
	rb := &ringBuffer{
		data: make([]byte, size),
		size: size,
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

// Write writes p into the ring buffer. If the buffer is full, it blocks
// on a condition variable until the audio callback drains some data.
// Returns io.EOF if the buffer has been closed.
// Called by the decoder goroutine.
func (rb *ringBuffer) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		rb.mu.Lock()
		for rb.used == rb.size && !rb.closed {
			rb.cond.Wait()
		}
		if rb.closed {
			rb.mu.Unlock()
			return written, io.EOF
		}
		avail := rb.size - rb.used
		n := len(p) - written
		if n > avail {
			n = avail
		}
		end := rb.wpos + n
		if end <= rb.size {
			copy(rb.data[rb.wpos:end], p[written:written+n])
		} else {
			first := rb.size - rb.wpos
			copy(rb.data[rb.wpos:], p[written:written+first])
			copy(rb.data[:n-first], p[written+first:written+n])
		}
		rb.wpos = (rb.wpos + n) % rb.size
		rb.used += n
		rb.mu.Unlock()
		written += n
	}
	return written, nil
}

// Read reads up to len(p) bytes from the ring buffer. If the buffer
// is empty, it fills p with zeros (silence) and returns len(p) — this
// is non-blocking to satisfy the audio callback's timing requirements.
// Returns the number of bytes that were actual audio data (not silence).
// Called by the malgo audio callback.
func (rb *ringBuffer) Read(p []byte) int {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	n := len(p)
	if n > rb.used {
		actual := rb.used
		if actual > 0 {
			rb.readLocked(p[:actual])
		}
		for i := actual; i < n; i++ {
			p[i] = 0
		}
		// Wake writer — space now available.
		rb.cond.Signal()
		return actual
	}

	rb.readLocked(p[:n])
	// Wake writer — space now available.
	rb.cond.Signal()
	return n
}

// readLocked reads exactly len(p) bytes from the ring, advancing rpos.
// Caller must hold rb.mu.
func (rb *ringBuffer) readLocked(p []byte) {
	n := len(p)
	end := rb.rpos + n
	if end <= rb.size {
		copy(p, rb.data[rb.rpos:end])
	} else {
		first := rb.size - rb.rpos
		copy(p[:first], rb.data[rb.rpos:])
		copy(p[first:], rb.data[:n-first])
	}
	rb.rpos = (rb.rpos + n) % rb.size
	rb.used -= n
}

// Available returns the number of bytes currently buffered.
func (rb *ringBuffer) Available() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.used
}

// Reset clears the ring buffer and reopens it for writing.
func (rb *ringBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.rpos = 0
	rb.wpos = 0
	rb.used = 0
	rb.closed = false
}

// Close signals writers to stop. Any blocked Write returns io.EOF.
func (rb *ringBuffer) Close() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.closed = true
	rb.cond.Broadcast()
}
