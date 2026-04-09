package player

import (
	"log/slog"

	"github.com/gen2brain/malgo"
)

// audioDevice wraps a malgo playback device with a ring buffer.
type audioDevice struct {
	ctx        *malgo.AllocatedContext
	device     *malgo.Device
	ring       *ringBuffer
	logger     *slog.Logger
	deviceName string // name of the output device
}

// newAudioDevice initializes a malgo playback device.
func newAudioDevice(sampleRate uint32, channels uint8, bitsPerSample uint8, logger *slog.Logger) (*audioDevice, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, err
	}

	// Determine malgo format from bits per sample.
	var format malgo.FormatType
	switch {
	case bitsPerSample <= 16:
		format = malgo.FormatS16
	case bitsPerSample <= 24:
		format = malgo.FormatS24
	default:
		format = malgo.FormatS32
	}

	// Ring buffer: ~10 seconds of audio. Large buffer absorbs Go
	// scheduler latency and GC pauses that can delay the decode
	// goroutine's cond.Wait wakeup after the ring drains.
	bytesPerSample := malgo.SampleSizeInBytes(format)
	ringSize := int(sampleRate) * int(channels) * int(bytesPerSample) * 10
	ring := newRingBuffer(ringSize)

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = format
	deviceConfig.Playback.Channels = uint32(channels)
	deviceConfig.SampleRate = sampleRate
	deviceConfig.PeriodSizeInFrames = 512  // ~11.6ms at 44100 Hz
	deviceConfig.Periods = 4               // 4 periods = ~46ms total buffer
	deviceConfig.Alsa.NoMMap = 1

	// Query the default playback device name.
	devName := "Unknown"
	if devs, err := ctx.Devices(malgo.Playback); err == nil {
		for _, d := range devs {
			if d.IsDefault != 0 {
				devName = d.Name()
				break
			}
		}
		// If no default flagged, use the first device.
		if devName == "Unknown" && len(devs) > 0 {
			devName = devs[0].Name()
		}
	}

	ad := &audioDevice{
		ctx:        ctx,
		ring:       ring,
		logger:     logger,
		deviceName: devName,
	}

	callbacks := malgo.DeviceCallbacks{
		Data: func(pOutput, pInput []byte, framecount uint32) {
			bytesNeeded := int(framecount) * int(channels) * int(bytesPerSample)
			if bytesNeeded > len(pOutput) {
				bytesNeeded = len(pOutput)
			}
			ad.ring.Read(pOutput[:bytesNeeded])
		},
	}

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, callbacks)
	if err != nil {
		ctx.Free()
		return nil, err
	}
	ad.device = device

	return ad, nil
}

// start begins audio playback.
func (ad *audioDevice) start() error {
	return ad.device.Start()
}

// stop halts audio playback. Closes the ring buffer first to unblock
// any writer (decoder goroutine) spinning on a full buffer.
func (ad *audioDevice) stop() {
	ad.ring.Close()
	ad.device.Stop()
}

// reset prepares the ring buffer for a new track.
func (ad *audioDevice) reset() {
	ad.ring.Reset()
}

// pause halts audio playback without clearing the buffer.
func (ad *audioDevice) pause() {
	ad.device.Stop()
}

// resume restarts audio playback.
func (ad *audioDevice) resume() {
	ad.device.Start()
}

// ringSize returns the ring buffer total capacity.
func ringSize(ad *audioDevice) int {
	if ad == nil || ad.ring == nil {
		return 0
	}
	return ad.ring.size
}

// close releases all audio resources.
func (ad *audioDevice) close() {
	ad.device.Uninit()
	ad.ctx.Free()
}

// audioInfo returns the negotiated audio device configuration.
func (ad *audioDevice) audioInfo() AudioDeviceInfo {
	format := "unknown"
	switch ad.device.PlaybackFormat() {
	case malgo.FormatU8:
		format = "U8"
	case malgo.FormatS16:
		format = "S16LE"
	case malgo.FormatS24:
		format = "S24LE"
	case malgo.FormatS32:
		format = "S32LE"
	case malgo.FormatF32:
		format = "F32LE"
	}
	return AudioDeviceInfo{
		DeviceName: ad.deviceName,
		SampleRate: ad.device.SampleRate(),
		Format:     format,
		Channels:   ad.device.PlaybackChannels(),
	}
}
