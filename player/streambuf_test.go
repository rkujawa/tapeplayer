package player

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestStreamBufferBasic(t *testing.T) {
	sb := newStreamBuffer()

	sb.Write([]byte("hello "))
	sb.Write([]byte("world"))
	sb.Complete()

	got, err := io.ReadAll(sb)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestStreamBufferBlocksUntilData(t *testing.T) {
	sb := newStreamBuffer()

	done := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(sb)
		done <- got
	}()

	// Reader should be blocking — no data yet.
	select {
	case <-done:
		t.Fatal("reader returned before any data written")
	case <-time.After(50 * time.Millisecond):
		// Good — reader is blocked.
	}

	sb.Write([]byte("delayed"))
	sb.Complete()

	got := <-done
	if string(got) != "delayed" {
		t.Fatalf("got %q, want %q", got, "delayed")
	}
}

func TestStreamBufferConcurrentWriteRead(t *testing.T) {
	sb := newStreamBuffer()

	// Writer: send 1000 chunks of 100 bytes each.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		chunk := bytes.Repeat([]byte("x"), 100)
		for range 1000 {
			sb.Write(chunk)
		}
		sb.Complete()
	}()

	got, err := io.ReadAll(sb)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 100000 {
		t.Fatalf("got %d bytes, want 100000", len(got))
	}
	wg.Wait()
}

func TestStreamBufferAbort(t *testing.T) {
	sb := newStreamBuffer()

	sb.Write([]byte("partial"))
	myErr := errors.New("tape error")
	sb.Abort(myErr)

	// Should read the buffered data first.
	buf := make([]byte, 100)
	n, err := sb.Read(buf)
	if err != nil {
		t.Fatalf("first Read should succeed, got: %v", err)
	}
	if string(buf[:n]) != "partial" {
		t.Fatalf("got %q, want %q", buf[:n], "partial")
	}

	// Next read should return the abort error.
	_, err = sb.Read(buf)
	if !errors.Is(err, myErr) {
		t.Fatalf("expected abort error, got: %v", err)
	}
}

func TestStreamBufferAbortUnblocksReader(t *testing.T) {
	sb := newStreamBuffer()

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 100)
		_, err := sb.Read(buf)
		done <- err
	}()

	// Reader should be blocking.
	select {
	case <-done:
		t.Fatal("reader returned before abort")
	case <-time.After(50 * time.Millisecond):
	}

	myErr := errors.New("connection lost")
	sb.Abort(myErr)

	err := <-done
	if !errors.Is(err, myErr) {
		t.Fatalf("expected abort error, got: %v", err)
	}
}

func TestStreamBufferPartialRead(t *testing.T) {
	sb := newStreamBuffer()

	sb.Write([]byte("ABCDEFGHIJ"))
	sb.Complete()

	// Read in 3-byte chunks.
	var got []byte
	buf := make([]byte, 3)
	for {
		n, err := sb.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if string(got) != "ABCDEFGHIJ" {
		t.Fatalf("got %q, want %q", got, "ABCDEFGHIJ")
	}
}

func TestStreamBufferBytesAfterComplete(t *testing.T) {
	sb := newStreamBuffer()
	sb.Write([]byte("full content"))
	sb.Complete()

	// Drain the reader.
	io.ReadAll(sb)

	// Bytes should return the full content for replay.
	if string(sb.Bytes()) != "full content" {
		t.Fatalf("Bytes: got %q", sb.Bytes())
	}
}

func TestStreamBufferLazyGrowth(t *testing.T) {
	sb := newStreamBuffer()

	// Initial capacity should be 4 MB.
	if cap(sb.data) != 4*1024*1024 {
		t.Fatalf("initial cap = %d, want %d", cap(sb.data), 4*1024*1024)
	}

	// Write 5 MB — capacity should double from 4 to 8 MB.
	chunk := make([]byte, 5*1024*1024)
	sb.Write(chunk)
	c := cap(sb.data)
	if c < 5*1024*1024 || c > 10*1024*1024 {
		t.Fatalf("cap after 5 MB write = %d, want between 5 MB and 10 MB", c)
	}

	// Write up to 70 MB total — verify capacity grew past initial.
	more := make([]byte, 65*1024*1024)
	sb.Write(more)
	if cap(sb.data) < 70*1024*1024 {
		t.Fatalf("cap after 70 MB = %d, want >= 70 MB", cap(sb.data))
	}
	sb.Complete()
}

func TestStreamBufferGrowthPolicy(t *testing.T) {
	sb := newStreamBuffer()

	// Write exactly 64 MB. Doubling: 4->8->16->32->64 MB.
	data := make([]byte, 64*1024*1024)
	sb.Write(data)
	c := cap(sb.data)
	if c != 64*1024*1024 {
		t.Fatalf("cap after 64 MB write = %d, want exactly 64 MB (%d)", c, 64*1024*1024)
	}

	// Write 1 more byte — should allocate 64+64 = 128 MB.
	sb.Write([]byte{0x42})
	c = cap(sb.data)
	if c != 128*1024*1024 {
		t.Fatalf("cap after 64 MB + 1 byte = %d, want 128 MB (%d)", c, 128*1024*1024)
	}
	sb.Complete()
}

func TestStreamBufferConcurrentBytes(t *testing.T) {
	sb := newStreamBuffer()

	var wg sync.WaitGroup

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		chunk := bytes.Repeat([]byte("y"), 1024)
		for range 1000 {
			sb.Write(chunk)
		}
		sb.Complete()
	}()

	// 100 concurrent Bytes() readers.
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				data := sb.Bytes()
				_ = len(data) // just access it
			}
		}()
	}

	wg.Wait()
}

func TestStreamBufferBytesBeforeComplete(t *testing.T) {
	sb := newStreamBuffer()
	sb.Write([]byte("hello"))

	// Bytes() should work even before Complete() — no panic.
	got := sb.Bytes()
	if string(got) != "hello" {
		t.Fatalf("Bytes before complete: got %q, want %q", got, "hello")
	}

	sb.Complete()

	// After complete too.
	got = sb.Bytes()
	if string(got) != "hello" {
		t.Fatalf("Bytes after complete: got %q, want %q", got, "hello")
	}
}

func TestStreamBufferNoPolling(t *testing.T) {
	sb := newStreamBuffer()

	// Write 1 byte from another goroutine after a brief delay.
	go func() {
		time.Sleep(10 * time.Millisecond)
		sb.Write([]byte{0x42})
	}()

	// Read should wake up immediately when data arrives (via notify),
	// not after a 10ms polling interval.
	buf := make([]byte, 1)
	start := time.Now()
	n, err := sb.Read(buf)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 1 || buf[0] != 0x42 {
		t.Fatalf("got n=%d buf=%x, want n=1 buf=42", n, buf[0])
	}
	// The write happens after ~10ms. Read should return within ~15ms
	// (10ms delay + near-instant wake). If polling at 10ms, it would
	// add another 10ms on average. Allow up to 50ms for CI jitter.
	if elapsed > 50*time.Millisecond {
		t.Fatalf("Read took %v, expected near-instant wake after Write", elapsed)
	}
}

func TestStreamBufferLen(t *testing.T) {
	sb := newStreamBuffer()
	if sb.Len() != 0 {
		t.Fatalf("initial Len = %d", sb.Len())
	}
	sb.Write([]byte("12345"))
	if sb.Len() != 5 {
		t.Fatalf("Len after write = %d", sb.Len())
	}
}
