#ifndef _AUDIOIO_H_
#define _AUDIOIO_H_

bool audio_format_set(uint8_t bits, uint32_t sample_rate, uint8_t channels);
bool audio_playback(const int32_t *const buffer[], uint16_t block_size);
bool audio_open();
bool audio_close();

#endif /* _AUDIOIO_H_ */
