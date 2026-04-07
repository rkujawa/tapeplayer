# tapeplayer

A TUI FLAC audio player for iSCSI-attached tape drives. Plays FLAC files directly from LTO, DDS, and other SCSI tape drives over iSCSI, with a tape deck-style terminal interface.

Built on [uiscsi](https://github.com/rkujawa/uiscsi) and [uiscsi-tape](https://github.com/rkujawa/uiscsi-tape).

## Features

- **Tape deck TUI** -- play/pause, stop, forward, back, rewind with bubbletea interface
- **Streaming playback** -- starts playing before the full file is buffered from tape (critical for slower DDS drives)
- **FLAC metadata** -- displays artist, album, title, format from Vorbis comments
- **Track history** -- "back" replays from memory without re-reading tape (last 3 tracks)
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
| f / Right | Next track |
| b / Left | Previous track (< 3s) or restart current |
| r | Rewind tape to beginning |
| q / Ctrl+C | Quit (second Ctrl+C force-quits) |

## Architecture

```
Tape Reader ──▶ streamBuffer ──▶ FLAC Decoder ──▶ Ring Buffer ──▶ Audio Device
(goroutine)    (blocking reader)  (goroutine)     (cond-var)     (malgo callback)
     │                │                │                              │
     └── Send() ──────┴── Send() ─────┘                              │
                       ▼                                              ▼
                 Bubbletea TUI ◀──────────────────────────────────────┘
```

- **streamBuffer**: growable buffer with blocking `io.Reader`. Tape fills it in background, FLAC decoder reads as data arrives. Enables playback before full file is buffered.
- **ringBuffer**: fixed-size PCM buffer between decoder and audio callback. Uses `sync.Cond` (no spin-waiting). Audio callback never blocks.
- **WaitGroup**: tracks background goroutines. `Stop()` waits for clean exit before starting next track.

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
