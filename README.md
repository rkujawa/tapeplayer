# tapeplayer

A TUI FLAC audio player for iSCSI-attached tape drives. Plays FLAC files directly from LTO, DDS, and other SCSI tape drives over iSCSI, with a tape deck-style terminal interface.

Built on [uiscsi](https://github.com/rkujawa/uiscsi) and [uiscsi-tape](https://github.com/rkujawa/uiscsi-tape).

## Features

- **Tape deck TUI** -- play/pause, stop, forward, back, rewind with bubbletea interface
- **Playlist discovery** -- builds a playlist as files are read from tape, showing track titles, sizes, and cache status
- **Streaming playback** -- starts playing before the full file is buffered from tape (critical for slower DDS drives)
- **LRU data cache** -- caches discovered tracks in memory (500MB default) for instant replay; metadata preserved even after eviction
- **FLAC metadata** -- displays artist, album, title, format from Vorbis comments
- **Hardware decompression** -- optional `-decompress` for tapes written with drive-level compression
- **Variable and fixed block modes** -- works with any tape block size via `-bs`
- **Debug logging** -- structured JSON log to file for diagnostics
- **Force quit** -- second Ctrl+C kills the process immediately (prevents unkillable processes from stuck audio drivers)

## Tape Format

Standard UNIX tape layout -- each FLAC file is written as a continuous stream of tape records, separated by filemarks. Double filemark marks end of tape:

```
[FLAC 1][filemark][FLAC 2][filemark]...[FLAC N][filemark][filemark]
```

Write FLACs to tape with:
```sh
for f in *.flac; do
    dd if="$f" bs=524288 > /dev/nst0
done
# Write double filemark (end of tape marker)
mt -f /dev/nst0 weof 2
```

## Usage

```sh
tapeplayer -portal 192.168.1.100:3260 -target iqn.example:tape -lun 2
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-portal` | (required) | iSCSI target portal (host:port) |
| `-target` | (required) | Target IQN |
| `-lun` | 0 | LUN number |
| `-initiator-name` | (auto) | Initiator IQN |
| `-bs` | 0 | Fixed block size in bytes (0 = variable block) |
| `-decompress` | false | Enable hardware decompression |
| `-debug` | (none) | Debug log file path |

### Controls

| Key | Action |
|-----|--------|
| Space / Enter | Play / Pause |
| s | Stop |
| f / Right | Next track (from cache if available, else reads tape) |
| b / Left | Previous track (< 3s) or restart current (always from cache) |
| r | Rewind tape to beginning |
| q / Ctrl+C | Quit (second Ctrl+C force-quits) |

## Architecture

```
                         ┌──────────┐
                         │ Playlist │ (LRU cache + metadata)
                         └────┬─────┘
                              │
Tape Reader ──▶ streamBuffer ──┤──▶ FLAC Decoder ──▶ Ring Buffer ──▶ Audio Device
(goroutine)    (blocking reader)│    (goroutine)     (cond-var)     (malgo callback)
                              │
                              ▼
                        Bubbletea TUI
```

### Key components

- **Playlist**: central data structure tracking all discovered tape files. Each entry holds FLAC metadata (artist, title, duration, size) and optionally cached data. LRU eviction keeps memory under the configured limit (500MB default). The currently playing track is never evicted. Metadata survives eviction, so the playlist always shows the full track list.

- **streamBuffer**: growable buffer with blocking `io.Reader`. Tape fills it in background while the FLAC decoder reads from it concurrently. Enables playback to start before the full file is buffered -- critical for DDS drives (~6 MB/s) where a 50MB file takes ~8 seconds to buffer.

- **ringBuffer**: fixed-size PCM sample buffer between the FLAC decoder and the audio callback. Uses `sync.Cond` (no spin-waiting). The audio callback never blocks -- it fills silence on underrun.

- **Navigation**: Forward/Back operate on the playlist index, not the tape head position. Cache hits are instant. If a track's data was evicted, the player rewinds the tape and re-reads (expensive, logged as warning).

- **WaitGroup**: tracks background goroutines (tape reader, FLAC decoder). `Stop()` waits for clean exit before starting the next track.

## Dependencies

| Library | Purpose |
|---------|---------|
| [uiscsi](https://github.com/rkujawa/uiscsi) | iSCSI initiator |
| [uiscsi-tape](https://github.com/rkujawa/uiscsi-tape) | SSC tape driver |
| [mewkiz/flac](https://github.com/mewkiz/flac) | FLAC decoding (pure Go) |
| [gen2brain/malgo](https://github.com/gen2brain/malgo) | Audio output (miniaudio) |
| [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) | TUI framework |
| [charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss) | TUI styling |

## Requirements

- Go 1.25 or later
- CGo (required by malgo for system audio)
- iSCSI-attached tape drive with loaded media
