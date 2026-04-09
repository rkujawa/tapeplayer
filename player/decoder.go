package player

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// flacDecoder wraps mewkiz/flac to provide frame-by-frame decoding
// with PCM sample conversion for the audio output.
type flacDecoder struct {
	stream  *flac.Stream
	logger  *slog.Logger
	samples uint64 // samples decoded so far (for position tracking)
}

// newFlacDecoder creates a FLAC decoder reading from r.
// The reader can be a streamBuffer (blocks until data available).
func newFlacDecoder(r io.Reader, logger *slog.Logger) (*flacDecoder, error) {
	stream, err := flac.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("flac.Parse: %w", err)
	}
	return &flacDecoder{
		stream: stream,
		logger: logger,
	}, nil
}

// trackInfo extracts metadata from the FLAC stream.
func (d *flacDecoder) trackInfo() TrackInfo {
	info := TrackInfo{
		SampleRate:   d.stream.Info.SampleRate,
		BitsPerSample: uint8(d.stream.Info.BitsPerSample),
		Channels:     uint8(d.stream.Info.NChannels),
		TotalSamples: d.stream.Info.NSamples,
	}

	// Extract Vorbis comments if present.
	for _, block := range d.stream.Blocks {
		if block.Body == nil {
			continue
		}
		if vc, ok := block.Body.(*meta.VorbisComment); ok {
			for _, tag := range vc.Tags {
				if len(tag) < 2 {
					continue
				}
				switch strings.ToUpper(tag[0]) {
				case "ARTIST":
					info.Artist = tag[1]
				case "ALBUM":
					info.Album = tag[1]
				case "TITLE":
					info.Title = tag[1]
				case "TRACKNUMBER":
					info.TrackNum = tag[1]
				}
			}
		}
	}

	return info
}

// nextFrame decodes the next FLAC frame and returns interleaved PCM
// bytes in the target format (S16LE, S24LE, or S32LE based on
// bits per sample). Returns io.EOF at end of stream.
func (d *flacDecoder) nextFrame() ([]byte, error) {
	frame, err := d.stream.ParseNext()
	if err != nil {
		return nil, err
	}

	nChannels := int(d.stream.Info.NChannels)
	bps := int(d.stream.Info.BitsPerSample)
	if len(frame.Subframes) < nChannels {
		return nil, fmt.Errorf("FLAC frame has %d subframes, expected %d channels", len(frame.Subframes), nChannels)
	}
	nSamples := len(frame.Subframes[0].Samples)

	d.samples += uint64(nSamples)

	// Determine output byte width.
	var bytesPerSample int
	switch {
	case bps <= 16:
		bytesPerSample = 2
	case bps <= 24:
		bytesPerSample = 3
	default:
		bytesPerSample = 4
	}

	// Interleave channels and convert to little-endian bytes.
	out := make([]byte, nSamples*nChannels*bytesPerSample)
	off := 0
	for i := range nSamples {
		for ch := range nChannels {
			sample := frame.Subframes[ch].Samples[i]
			switch bytesPerSample {
			case 2:
				// Truncate/extend to 16-bit.
				s16 := int16(sample)
				if bps < 16 {
					s16 = int16(sample << (16 - bps))
				}
				binary.LittleEndian.PutUint16(out[off:], uint16(s16))
			case 3:
				// 24-bit little-endian.
				s32 := int32(sample)
				if bps < 24 {
					s32 = int32(sample << (24 - bps))
				}
				out[off] = byte(s32)
				out[off+1] = byte(s32 >> 8)
				out[off+2] = byte(s32 >> 16)
			case 4:
				// 32-bit little-endian.
				s32 := int32(sample)
				if bps < 32 {
					s32 = int32(sample << (32 - bps))
				}
				binary.LittleEndian.PutUint32(out[off:], uint32(s32))
			}
			off += bytesPerSample
		}
	}

	return out, nil
}

// isComplete reports whether all expected samples have been decoded.
// Returns true if NSamples is 0 (unknown) or all samples were decoded.
func (d *flacDecoder) isComplete() bool {
	return d.stream.Info.NSamples == 0 || d.samples >= d.stream.Info.NSamples
}

// totalSamples returns the expected total from STREAMINFO (0 if unknown).
func (d *flacDecoder) totalSamples() uint64 {
	return d.stream.Info.NSamples
}

// position returns the current playback position based on decoded samples.
func (d *flacDecoder) position() time.Duration {
	if d.stream.Info.SampleRate == 0 {
		return 0
	}
	return time.Duration(d.samples) * time.Second / time.Duration(d.stream.Info.SampleRate)
}
