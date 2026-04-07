package player

import (
	"sync"
	"time"
)

const defaultMaxCache int64 = 500 * 1024 * 1024 // 500MB

// PlaylistEntry represents a discovered file on tape.
type PlaylistEntry struct {
	Index    int
	Info     TrackInfo // FLAC metadata (zero value until decoded)
	Size     int64     // total FLAC data size in bytes
	Cached   bool      // true if data is in memory (for UI display)
	data     []byte    // cached FLAC data (nil if evicted by LRU)
	lastUsed time.Time // last time data was accessed (for LRU)
}

// Playlist tracks discovered tape files, caches their data with LRU
// eviction, and provides the logical navigation model for the player.
type Playlist struct {
	mu         sync.Mutex
	entries    []*PlaylistEntry
	current    int   // index of currently playing track (-1 if none)
	eot        bool  // true if end-of-tape marker seen
	cacheUsed  int64 // total bytes currently cached
	cacheLimit int64 // max cache size
	tapeHead   int   // index of next undiscovered file on tape
}

// NewPlaylist creates a playlist with the given cache size limit.
// Use 0 for the default (500MB).
func NewPlaylist(cacheLimit int64) *Playlist {
	if cacheLimit <= 0 {
		cacheLimit = defaultMaxCache
	}
	return &Playlist{
		current:    -1,
		cacheLimit: cacheLimit,
	}
}

// Add appends a newly discovered file to the playlist with its data
// cached. Evicts LRU entries if the cache exceeds the limit. Returns
// the index of the new entry.
func (pl *Playlist) Add(data []byte, info TrackInfo) int {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx := len(pl.entries)
	// Take ownership of data — caller must not use the slice after Add.
	entry := &PlaylistEntry{
		Index:    idx,
		Info:     info,
		Size:     int64(len(data)),
		data:     data,
		lastUsed: time.Now(),
	}
	pl.entries = append(pl.entries, entry)
	pl.cacheUsed += entry.Size
	pl.tapeHead = idx + 1

	pl.evictLocked()

	return idx
}

// MarkEOT marks end of tape — no more entries will be discovered.
func (pl *Playlist) MarkEOT() {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.eot = true
}

// Get returns the entry at index, or nil if out of range.
func (pl *Playlist) Get(index int) *PlaylistEntry {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if index < 0 || index >= len(pl.entries) {
		return nil
	}
	return pl.entries[index]
}

// SetCurrent sets the currently playing track index.
func (pl *Playlist) SetCurrent(index int) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.current = index
}

// Current returns the currently playing track index (-1 if none).
func (pl *Playlist) Current() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.current
}

// Len returns the number of discovered entries.
func (pl *Playlist) Len() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return len(pl.entries)
}

// IsEOT returns true if end of tape was seen.
func (pl *Playlist) IsEOT() bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.eot
}

// IsCached returns true if the entry at index has data in memory.
func (pl *Playlist) IsCached(index int) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if index < 0 || index >= len(pl.entries) {
		return false
	}
	return pl.entries[index].data != nil
}

// Data returns the cached FLAC data for the entry at index.
// Returns nil if evicted or out of range. Updates lastUsed for LRU.
func (pl *Playlist) Data(index int) []byte {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if index < 0 || index >= len(pl.entries) {
		return nil
	}
	e := pl.entries[index]
	if e.data != nil {
		e.lastUsed = time.Now()
	}
	return e.data
}

// TapeHead returns the index of the next file to be discovered from
// tape. This is where the physical tape head is positioned (the file
// after the last one we read).
func (pl *Playlist) TapeHead() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.tapeHead
}

// Snapshot returns a copy of all entries for UI display, plus the
// current index and EOT flag.
func (pl *Playlist) Snapshot() (entries []PlaylistEntry, current int, eot bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	out := make([]PlaylistEntry, len(pl.entries))
	for i, e := range pl.entries {
		out[i] = PlaylistEntry{
			Index:  e.Index,
			Info:   e.Info,
			Size:   e.Size,
			Cached: e.data != nil,
		}
	}
	return out, pl.current, pl.eot
}

// UpdateInfo sets the TrackInfo for an entry (called when FLAC
// metadata is parsed, which happens after Add).
func (pl *Playlist) UpdateInfo(index int, info TrackInfo) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if index >= 0 && index < len(pl.entries) {
		pl.entries[index].Info = info
	}
}

// evictLocked removes the least recently used cached entries until
// cacheUsed <= cacheLimit. Never evicts the current track.
// Caller must hold pl.mu.
func (pl *Playlist) evictLocked() {
	for pl.cacheUsed > pl.cacheLimit {
		victim := -1
		var oldest time.Time
		for i, e := range pl.entries {
			if e.data == nil || i == pl.current {
				continue
			}
			if victim == -1 || e.lastUsed.Before(oldest) {
				victim = i
				oldest = e.lastUsed
			}
		}
		if victim == -1 {
			break // nothing left to evict
		}
		pl.cacheUsed -= pl.entries[victim].Size
		pl.entries[victim].data = nil
	}
}
