package player

import (
	"testing"
)

func TestPlaylistAdd(t *testing.T) {
	pl := NewPlaylist(0)

	idx := pl.Add([]byte("track1data"), TrackInfo{Title: "Track 1"})
	if idx != 0 {
		t.Errorf("first Add: got index %d, want 0", idx)
	}
	if pl.Len() != 1 {
		t.Errorf("Len: got %d, want 1", pl.Len())
	}

	idx = pl.Add([]byte("track2data"), TrackInfo{Title: "Track 2"})
	if idx != 1 {
		t.Errorf("second Add: got index %d, want 1", idx)
	}
	if pl.Len() != 2 {
		t.Errorf("Len: got %d, want 2", pl.Len())
	}

	e := pl.Get(0)
	if e == nil || e.Info.Title != "Track 1" {
		t.Errorf("Get(0): unexpected entry %+v", e)
	}
	e = pl.Get(1)
	if e == nil || e.Info.Title != "Track 2" {
		t.Errorf("Get(1): unexpected entry %+v", e)
	}
	if pl.Get(2) != nil {
		t.Error("Get(2): expected nil for out of range")
	}
}

func TestPlaylistLRU(t *testing.T) {
	// Cache limit of 20 bytes.
	pl := NewPlaylist(20)

	pl.Add(make([]byte, 10), TrackInfo{Title: "A"}) // 10 bytes, total 10
	pl.Add(make([]byte, 10), TrackInfo{Title: "B"}) // 10 bytes, total 20
	// Both should be cached.
	if !pl.IsCached(0) {
		t.Error("entry 0 should be cached")
	}
	if !pl.IsCached(1) {
		t.Error("entry 1 should be cached")
	}

	// Add a third — should evict the oldest (entry 0).
	pl.Add(make([]byte, 10), TrackInfo{Title: "C"}) // total would be 30, evict oldest
	if pl.IsCached(0) {
		t.Error("entry 0 should have been evicted")
	}
	if !pl.IsCached(1) {
		t.Error("entry 1 should still be cached")
	}
	if !pl.IsCached(2) {
		t.Error("entry 2 should be cached")
	}
}

func TestPlaylistLRUKeepsCurrent(t *testing.T) {
	pl := NewPlaylist(20)

	pl.Add(make([]byte, 10), TrackInfo{Title: "A"})
	pl.Add(make([]byte, 10), TrackInfo{Title: "B"})
	pl.SetCurrent(0) // protect entry 0

	// Add a third — should evict entry 1 (oldest non-current), not entry 0.
	pl.Add(make([]byte, 10), TrackInfo{Title: "C"})
	if !pl.IsCached(0) {
		t.Error("current entry 0 should NOT be evicted")
	}
	if pl.IsCached(1) {
		t.Error("entry 1 should be evicted (oldest non-current)")
	}
	if !pl.IsCached(2) {
		t.Error("entry 2 should be cached")
	}
}

func TestPlaylistEOT(t *testing.T) {
	pl := NewPlaylist(0)

	if pl.IsEOT() {
		t.Error("should not be EOT initially")
	}
	pl.MarkEOT()
	if !pl.IsEOT() {
		t.Error("should be EOT after MarkEOT")
	}
}

func TestPlaylistData(t *testing.T) {
	pl := NewPlaylist(0)

	data := []byte("hello playlist")
	pl.Add(data, TrackInfo{})

	got := pl.Data(0)
	if string(got) != "hello playlist" {
		t.Errorf("Data: got %q, want %q", got, "hello playlist")
	}

	// Out of range.
	if pl.Data(5) != nil {
		t.Error("Data(5): expected nil for out of range")
	}
}

func TestPlaylistDataEvicted(t *testing.T) {
	pl := NewPlaylist(15)

	pl.Add(make([]byte, 10), TrackInfo{Title: "A"})
	pl.Add(make([]byte, 10), TrackInfo{Title: "B"}) // evicts A

	// A's data is gone but metadata is preserved.
	if pl.Data(0) != nil {
		t.Error("entry 0 data should be nil (evicted)")
	}
	e := pl.Get(0)
	if e == nil {
		t.Fatal("entry 0 should still exist")
	}
	if e.Info.Title != "A" {
		t.Errorf("entry 0 metadata: got %q, want %q", e.Info.Title, "A")
	}
	if e.Size != 10 {
		t.Errorf("entry 0 Size: got %d, want 10", e.Size)
	}
}

func TestPlaylistTapeHead(t *testing.T) {
	pl := NewPlaylist(0)

	if pl.TapeHead() != 0 {
		t.Errorf("initial TapeHead: got %d, want 0", pl.TapeHead())
	}
	pl.Add([]byte("a"), TrackInfo{})
	if pl.TapeHead() != 1 {
		t.Errorf("TapeHead after 1 add: got %d, want 1", pl.TapeHead())
	}
	pl.Add([]byte("b"), TrackInfo{})
	if pl.TapeHead() != 2 {
		t.Errorf("TapeHead after 2 adds: got %d, want 2", pl.TapeHead())
	}
}

func TestPlaylistCurrent(t *testing.T) {
	pl := NewPlaylist(0)

	if pl.Current() != -1 {
		t.Errorf("initial Current: got %d, want -1", pl.Current())
	}
	pl.SetCurrent(2)
	if pl.Current() != 2 {
		t.Errorf("Current: got %d, want 2", pl.Current())
	}
}

func TestPlaylistSnapshot(t *testing.T) {
	pl := NewPlaylist(0)

	pl.Add([]byte("data1"), TrackInfo{Title: "Song 1"})
	pl.Add([]byte("data2"), TrackInfo{Title: "Song 2"})
	pl.SetCurrent(1)

	entries, current, eot := pl.Snapshot()
	if len(entries) != 2 {
		t.Fatalf("Snapshot entries: got %d, want 2", len(entries))
	}
	if current != 1 {
		t.Errorf("Snapshot current: got %d, want 1", current)
	}
	if eot {
		t.Error("Snapshot eot: should be false")
	}
	if entries[0].Info.Title != "Song 1" {
		t.Errorf("entry 0 title: got %q", entries[0].Info.Title)
	}
	if !entries[0].Cached {
		t.Error("snapshot entry 0 should indicate cached")
	}
}

func TestPlaylistUpdateInfo(t *testing.T) {
	pl := NewPlaylist(0)
	pl.Add([]byte("data"), TrackInfo{})

	pl.UpdateInfo(0, TrackInfo{Title: "Updated", Artist: "Test"})
	e := pl.Get(0)
	if e.Info.Title != "Updated" || e.Info.Artist != "Test" {
		t.Errorf("UpdateInfo: got %+v", e.Info)
	}
}
