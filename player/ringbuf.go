package player

import "sync"

// ringBuffer is a fixed-size lock-free-ish circular buffer for PCM audio
// samples. The decoder goroutine writes decoded samples, and the malgo
// audio callback reads them. Both sides coordinate via a mutex — the
// callback must not block for long, so reads are non-blocking (return
// silence on underrun).
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	size int
	rpos int // read position
	wpos int // write position
	used int // bytes currently buffered
}

// newRingBuffer creates a ring buffer with the given capacity in bytes.
func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		data: make([]byte, size),
		size: size,
	}
}

// Write writes p into the ring buffer. If the buffer is full, it blocks
// until space is available. Returns the number of bytes written.
// Called by the decoder goroutine.
func (rb *ringBuffer) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		rb.mu.Lock()
		avail := rb.size - rb.used
		if avail == 0 {
			rb.mu.Unlock()
			// Spin briefly — the audio callback will drain soon.
			// In practice, this yields the goroutine.
			continue
		}
		n := len(p) - written
		if n > avail {
			n = avail
		}
		// Write up to end of buffer, then wrap.
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
		// Underrun: fill what we have, zero the rest.
		actual := rb.used
		rb.readLocked(p[:actual])
		// Zero-fill the remainder (silence).
		for i := actual; i < n; i++ {
			p[i] = 0
		}
		return actual
	}

	rb.readLocked(p[:n])
	return n
}

// readLocked reads exactly n bytes from the ring, advancing rpos.
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

// Reset clears the ring buffer.
func (rb *ringBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.rpos = 0
	rb.wpos = 0
	rb.used = 0
}
