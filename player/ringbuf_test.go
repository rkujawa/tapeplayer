package player

import (
	"sync"
	"testing"
)

func TestRingBufferBasic(t *testing.T) {
	rb := newRingBuffer(1024)

	data := []byte("hello ring")
	rb.Write(data)

	buf := make([]byte, len(data))
	n := rb.Read(buf)
	if n != len(data) {
		t.Errorf("Read returned %d actual bytes, want %d", n, len(data))
	}
	if string(buf[:n]) != "hello ring" {
		t.Errorf("got %q, want %q", buf[:n], "hello ring")
	}
}

func TestRingBufferUnderrun(t *testing.T) {
	rb := newRingBuffer(1024)

	// Read from empty buffer — should get silence (zeros).
	buf := make([]byte, 100)
	n := rb.Read(buf)
	if n != 0 {
		t.Errorf("actual bytes from empty buffer = %d, want 0", n)
	}
	for i, b := range buf {
		if b != 0 {
			t.Errorf("buf[%d] = 0x%02X, want 0x00 (silence)", i, b)
			break
		}
	}
}

func TestRingBufferPartialUnderrun(t *testing.T) {
	rb := newRingBuffer(1024)

	rb.Write([]byte{0xAA, 0xBB, 0xCC})

	buf := make([]byte, 6)
	n := rb.Read(buf)
	if n != 3 {
		t.Errorf("actual bytes = %d, want 3", n)
	}
	// First 3 bytes should be data.
	if buf[0] != 0xAA || buf[1] != 0xBB || buf[2] != 0xCC {
		t.Errorf("data bytes: got %v", buf[:3])
	}
	// Remaining 3 should be silence.
	if buf[3] != 0 || buf[4] != 0 || buf[5] != 0 {
		t.Errorf("silence bytes: got %v", buf[3:6])
	}
}

func TestRingBufferWrapAround(t *testing.T) {
	rb := newRingBuffer(8)

	// Fill buffer.
	rb.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8})

	// Read 4 bytes — frees first half.
	buf := make([]byte, 4)
	rb.Read(buf)
	if buf[0] != 1 || buf[3] != 4 {
		t.Errorf("first read: got %v", buf)
	}

	// Write 4 more — wraps around.
	rb.Write([]byte{9, 10, 11, 12})

	// Read all 8 buffered bytes.
	buf = make([]byte, 8)
	n := rb.Read(buf)
	if n != 8 {
		t.Errorf("actual = %d, want 8", n)
	}
	want := []byte{5, 6, 7, 8, 9, 10, 11, 12}
	for i := range want {
		if buf[i] != want[i] {
			t.Errorf("buf[%d] = %d, want %d", i, buf[i], want[i])
		}
	}
}

func TestRingBufferConcurrent(t *testing.T) {
	rb := newRingBuffer(4096)

	var wg sync.WaitGroup
	wg.Add(1)

	// Writer: 10000 bytes in 100-byte chunks.
	go func() {
		defer wg.Done()
		chunk := make([]byte, 100)
		for i := range chunk {
			chunk[i] = 0xFF
		}
		for range 100 {
			rb.Write(chunk)
		}
	}()

	// Reader: drain until we've read 10000 bytes of actual data.
	totalActual := 0
	buf := make([]byte, 256)
	for totalActual < 10000 {
		n := rb.Read(buf)
		totalActual += n
	}

	wg.Wait()

	if totalActual != 10000 {
		t.Errorf("total actual = %d, want 10000", totalActual)
	}
}

func TestRingBufferReset(t *testing.T) {
	rb := newRingBuffer(1024)
	rb.Write([]byte("data"))
	rb.Reset()

	if rb.Available() != 0 {
		t.Errorf("Available after Reset = %d, want 0", rb.Available())
	}
}
