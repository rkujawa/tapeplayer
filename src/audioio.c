#include <stdint.h>
#include <stdio.h>
#include <stdbool.h>


#include <ao/ao.h>
#include <gc/gc.h>

#include <assert.h>

static ao_device *ao_dev;
static ao_sample_format ao_fmt;

bool
audio_format_set(uint8_t bits, uint32_t sample_rate, uint8_t channels)
{
	// TODO: validate
	ao_fmt.bits = bits;
	ao_fmt.rate = sample_rate;
	ao_fmt.channels = channels;
	ao_fmt.byte_format = AO_FMT_NATIVE;

	fprintf(stderr, "audio: set format %d bits, %d channels, %d rate\n",
	    ao_fmt.bits, ao_fmt.rate, ao_fmt.channels);

	return true;
}

bool
audio_playback(const int32_t *const buffer[], uint16_t block_size) 
{
	// improve error handling...

	size_t playback_size;
	int sample, channel, i;

	uint8_t *playback_buf, *playback_buf_u8;
	//int16_t *playback_buf_s16;

	playback_size = block_size * ao_fmt.channels * ao_fmt.bits / 8;

	playback_buf = GC_malloc(playback_size);
	playback_buf_u8 = (uint8_t*) playback_buf;
	//playback_buf_s16 = (int16_t*) playback_buf;

	assert(ao_fmt.bits == 24); // XXX

	{ /* stolen from flac123 */
		for (sample = i = 0; sample < block_size; sample++) {
			for (channel = 0; channel < ao_fmt.channels; channel++,i+=3) {
				int32_t scaled_sample = (int32_t) (buffer[channel][sample] * ((float)1));
				playback_buf_u8[i]   = (scaled_sample >>  0) & 0xFF;
				playback_buf_u8[i+1] = (scaled_sample >>  8) & 0xFF;
				playback_buf_u8[i+2] = (scaled_sample >> 16) & 0xFF;
			}
		}
	}

	ao_play(ao_dev, (char *)playback_buf, playback_size);

	fprintf(stderr, "audio: played block (block_size %u, samplerate %u, channels %u, %u bits, playback buf size %zu)\n", block_size, ao_fmt.rate, ao_fmt.channels, ao_fmt.bits, playback_size);
	return true;
}

bool
audio_open()
{
	int ao_output_id;

	ao_output_id = ao_default_driver_id();
	ao_dev = ao_open_live(ao_output_id, &ao_fmt, NULL);
	assert(ao_dev);

	return true;
}

bool
audio_close()
{
	return ao_close(ao_dev);
}
