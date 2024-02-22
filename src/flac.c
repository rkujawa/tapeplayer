#include <stdio.h>
#include <stdint.h>
#include <assert.h>
#include <string.h>

#include <sys/errno.h>
#include <unistd.h>

#include <FLAC/stream_decoder.h>
#include <FLAC/metadata.h>

#include <ao/ao.h>

#include <gc/gc.h>

#include "buffer.h"
#include "audioio.h"

static size_t flac_io_read(void *ptr, size_t size, size_t nmemb, FLAC__IOHandle handle);
static int flac_io_seek(FLAC__IOHandle handle, FLAC__int64 offset, int whence);
static FLAC__int64 flac_io_tell(FLAC__IOHandle handle);

static FLAC__IOCallbacks io_meta_cb = {
	.read = flac_io_read,
	.write = NULL,
	.seek = flac_io_seek,
	.tell = flac_io_tell,
	.eof = NULL,
	.close = NULL
};

//ao_device *ao_dev;
//ao_sample_format ao_fmt;  /* temporarily... */

static void
flac_metadata_vorbis_comment_dump(const FLAC__StreamMetadata *metablock)
{
	FLAC__uint32 i;

	for (i = 0; i < metablock->data.vorbis_comment.num_comments; i++) {
		fprintf(stderr, "flac: vorbis_comment %s\n", (char *) 
		    metablock->data.vorbis_comment.comments[i].entry);
	}
}

static size_t 
flac_io_read(void *ptr, size_t size, size_t nmemb, FLAC__IOHandle handle)
{
	ssize_t read_size;
	struct buffer *b;

	b = (struct buffer *) handle;

	//fprintf(stderr, "flac_io: read %zu * %zu from %p (pos %zu) to %p\n", size, nmemb, b->rptr, b->rpos, ptr);

	// handle going out of the buffer and end of data
	read_size = size * nmemb;

	// memcpy does not return errors, so how to check for errors?
	memcpy(ptr, b->rptr, read_size);
	
//	b->data += read_size;
	b->rptr += read_size;
	b->rpos += read_size;

	/* error handling could use improvement, considering the fact that we 
	 * wrap this in flac_io_decoder_read()... */

	return read_size;
}

static int
flac_io_seek(FLAC__IOHandle handle, FLAC__int64 offset, int whence) {

	struct buffer *b;

	b = (struct buffer *) handle;

	switch(whence) {
	case SEEK_SET:
		fprintf(stderr, "flac_io: seek set to %ld\n", offset);
		if (offset < 0)
			return -1;
		if (offset > b->size)
			return -1;

		b->rpos = offset;
		b->rptr = b->rptr + offset;
		break;
	case SEEK_END:
		fprintf(stderr, "flac_io: seek end to %ld\n", offset);
		b->rpos = b->size;
		b->rptr = &(b->data[b->size]);
	case SEEK_CUR:
		fprintf(stderr, "flac_io: seek cur to %ld\n", offset);
		if ((b->rpos += offset) < 0)
			return -1;
		if ((b->rpos += offset) > b->size)
			return -1;

		b->rptr += offset;
		b->rpos += offset;	
		break;
	}

	return 0;
}

static FLAC__int64
flac_io_tell(FLAC__IOHandle handle)
{
	struct buffer *b;

	b = (struct buffer *) handle;

	//fprintf(stderr, "flac_io: telling rpos %zu rptr %p\n", b->rpos, b->rptr);

	return b->rpos;
}

static FLAC__StreamDecoderReadStatus
flac_io_decoder_read(const FLAC__StreamDecoder *decoder, FLAC__byte flacbuf[], size_t *bytes,
    void *client_data)
{
	struct buffer *b;

	b = (struct buffer *) client_data;

	if (*bytes > 0) {
		*bytes = flac_io_read(flacbuf, sizeof(FLAC__byte), *bytes, b);
		if (*bytes == -1) 
			return FLAC__STREAM_DECODER_READ_STATUS_ABORT;
		else if (*bytes == 0)
			return FLAC__STREAM_DECODER_READ_STATUS_END_OF_STREAM;
		else
			return FLAC__STREAM_DECODER_READ_STATUS_CONTINUE;
	} else
		return FLAC__STREAM_DECODER_READ_STATUS_ABORT;
}

static FLAC__StreamDecoderSeekStatus
flac_io_decoder_seek(const FLAC__StreamDecoder *decoder, FLAC__uint64 abs_byte_offset, 
    void *client_data)
{
	struct buffer *b;

	b = (struct buffer *) client_data;
	
	//return FLAC__STREAM_DECODER_SEEK_STATUS_UNSUPPORTED;

	if (flac_io_seek(b, abs_byte_offset, SEEK_SET) < 0)
		return FLAC__STREAM_DECODER_SEEK_STATUS_ERROR;
	else
		return FLAC__STREAM_DECODER_SEEK_STATUS_OK;
}

static FLAC__bool
flac_io_decoder_eof(const FLAC__StreamDecoder *decoder, void *client_data)
{
	// check buffer etc.
	return false;
}

static FLAC__StreamDecoderTellStatus
flac_io_decoder_tell(const FLAC__StreamDecoder *decoder, FLAC__uint64 *abs_byte_offset, 
    void *client_data)
{
	struct buffer *b;
	FLAC__uint64 pos;

	b = (struct buffer *) client_data;

	pos = flac_io_tell(b);

//	return FLAC__STREAM_DECODER_TELL_STATUS_UNSUPPORTED;

	if(pos < 0)
		return FLAC__STREAM_DECODER_TELL_STATUS_ERROR;
	else 
		*abs_byte_offset = pos;

	return FLAC__STREAM_DECODER_TELL_STATUS_OK;
}

static FLAC__StreamDecoderLengthStatus
flac_io_decoder_length(const FLAC__StreamDecoder *decoder, FLAC__uint64 *stream_length, 
    void *client_data)
{
//	struct buffer *b;

//	b = (struct buffer *) client_data;
	
	return FLAC__STREAM_DECODER_LENGTH_STATUS_UNSUPPORTED;
/*
	if (b->used == 0)
		return FLAC__STREAM_DECODER_LENGTH_STATUS_ERROR;
	else // what if "used" is changing due to data being written in another thread?
		*stream_length = (FLAC__uint64)b->used;

	return FLAC__STREAM_DECODER_LENGTH_STATUS_OK;
*/
}

static FLAC__StreamDecoderWriteStatus
flac_io_decoder_write(const FLAC__StreamDecoder *decoder, const FLAC__Frame *frame, 
    const FLAC__int32 *const buffer[], void *client_data)
{
	//if
	audio_playback(buffer, frame->header.blocksize);

	return FLAC__STREAM_DECODER_WRITE_STATUS_CONTINUE;
	// else abort

}


static void 
flac_io_decoder_metadata(const FLAC__StreamDecoder *decoder, const FLAC__StreamMetadata 
    *metadata, void *client_data)
{
    //FLAC_music *data = (FLAC_music *)client_data;

	if (metadata->type == FLAC__METADATA_TYPE_STREAMINFO) {
		audio_format_set(metadata->data.stream_info.bits_per_sample,
		    metadata->data.stream_info.sample_rate, 
		    metadata->data.stream_info.channels);
		// update ui with song info
	}

	if (metadata->type == FLAC__METADATA_TYPE_VORBIS_COMMENT) {
		flac_metadata_vorbis_comment_dump(metadata);
		fprintf(stderr, "flac_io: VORBIS_COMMENT\n");
		// update ui with song info
	}

/*
        data->flac_data.total_samples =
                            metadata->data.stream_info.total_samples;
        data->flac_data.sample_size = data->flac_data.channels *
                                        ((data->flac_data.bits_per_sample) / 8);*/
}

static void 
flac_io_error(const FLAC__StreamDecoder *decoder, FLAC__StreamDecoderErrorStatus status,
    void *client_data)
{
	fprintf(stderr, "flac_io: Decoding error %d\n", status);
	//switch (status) {
		/*
        case FLAC__STREAM_DECODER_ERROR_STATUS_LOST_SYNC:
            SDL_SetError ("Error processing the FLAC file [LOST_SYNC].");
        break;
        case FLAC__STREAM_DECODER_ERROR_STATUS_BAD_HEADER:
            SDL_SetError ("Error processing the FLAC file [BAD_HEADER].");
        break;
        case FLAC__STREAM_DECODER_ERROR_STATUS_FRAME_CRC_MISMATCH:
            SDL_SetError ("Error processing the FLAC file [CRC_MISMATCH].");
        break;
        case FLAC__STREAM_DECODER_ERROR_STATUS_UNPARSEABLE_STREAM:
            SDL_SetError ("Error processing the FLAC file [UNPARSEABLE].");
        break;
        default:
            SDL_SetError ("Error processing the FLAC file [UNKNOWN].");
        break;
    }*/
}


void 
flac_meta_test(struct buffer *b)
{
	FLAC__Metadata_Iterator *iterator;
	FLAC__Metadata_Chain *chain;
	FLAC__StreamMetadata *metablock;
	FLAC__bool rv;

	assert(b != NULL);
	assert(b->data != NULL);
	assert(b->size > 0);
	assert(b->blocks > 0);

	b->rpos = 0;
	b->rptr = b->data;

	chain = FLAC__metadata_chain_new();
	assert(chain);

	rv = FLAC__metadata_chain_read_with_callbacks(chain, (FLAC__IOHandle)b, io_meta_cb);

	if (!rv) {
		fprintf(stderr, "flac: failed to read metadata\n");
		return;
	}

	iterator = FLAC__metadata_iterator_new();
	assert(iterator);

	FLAC__metadata_iterator_init(iterator, chain);
	do {
		metablock = FLAC__metadata_iterator_get_block (iterator);
		if (metablock->type == FLAC__METADATA_TYPE_VORBIS_COMMENT) {
			fprintf(stderr, "flac: found Vorbis comments block\n");
			flac_metadata_vorbis_comment_dump(metablock);

		}
	} while (FLAC__metadata_iterator_next(iterator));

	FLAC__metadata_iterator_delete(iterator);
	FLAC__metadata_chain_delete(chain);

}

void
flac_test(struct buffer *b)
{
	FLAC__StreamDecoder* f;
	FLAC__StreamDecoderState state;

	uint32_t i;
	uint32_t frames_to_decode;

	i = 0;
	frames_to_decode = 5000; // XXX

	b->rpos = 0;
	b->rptr = b->data;
	
	f = FLAC__stream_decoder_new();

assert(FLAC__stream_decoder_set_metadata_respond(f, FLAC__METADATA_TYPE_VORBIS_COMMENT));

	FLAC__stream_decoder_init_stream(f, flac_io_decoder_read, flac_io_decoder_seek,
	    flac_io_decoder_tell, flac_io_decoder_length, flac_io_decoder_eof,
	    flac_io_decoder_write, flac_io_decoder_metadata, flac_io_error, b);


	FLAC__stream_decoder_process_until_end_of_metadata(f);

	audio_open();

	while (i < frames_to_decode) {

		if (!FLAC__stream_decoder_process_single(f)) {
			fprintf(stderr, "flac: failed decoding, returning\n");
			return;
		} else {
			//state = FLAC__stream_decoder_get_state(f);
			//fprintf(stderr, "flac: decoder state %d\n", state);
			;;
		}
		i++;
	}

	audio_close();

	fprintf(stderr, "flac: returning from flac_test\n");
}
