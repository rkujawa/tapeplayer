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
