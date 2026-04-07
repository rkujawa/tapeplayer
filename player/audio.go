package player

import (
	"log/slog"

	"github.com/gen2brain/malgo"
)

// audioDevice wraps a malgo playback device with a ring buffer.
type audioDevice struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
	ring   *ringBuffer
	logger *slog.Logger
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

	// Ring buffer: ~2 seconds of audio.
	bytesPerSample := malgo.SampleSizeInBytes(format)
	ringSize := int(sampleRate) * int(channels) * int(bytesPerSample) * 2
	ring := newRingBuffer(ringSize)

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = format
	deviceConfig.Playback.Channels = uint32(channels)
	deviceConfig.SampleRate = sampleRate
	deviceConfig.Alsa.NoMMap = 1

	ad := &audioDevice{
		ctx:    ctx,
		ring:   ring,
		logger: logger,
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
func (ad *audioDevice) start() {
	if err := ad.device.Start(); err != nil {
		ad.logger.Error("audio: start failed", "err", err)
	}
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

// close releases all audio resources.
func (ad *audioDevice) close() {
	ad.device.Uninit()
	ad.ctx.Free()
}
