#include <stdio.h>
#include <stdint.h>
#include <assert.h>
#include <string.h>

#include <sys/errno.h>

#include <FLAC/stream_decoder.h>
#include <FLAC/metadata.h>

#include "buffer.h"

static size_t flac_io_read(void *ptr, size_t size, size_t nmemb, FLAC__IOHandle handle);
static int flac_io_seek(FLAC__IOHandle handle, FLAC__int64 offset, int whence);
static FLAC__int64 flac_io_tell(FLAC__IOHandle handle);

static FLAC__IOCallbacks iocb = {
	.read = flac_io_read,
	.write = NULL,
	.seek = flac_io_seek,
	.tell = flac_io_tell,
	.eof = NULL,
	.close = NULL
};

static size_t 
flac_io_read(void *ptr, size_t size, size_t nmemb, FLAC__IOHandle handle)
{
	fprintf(stderr, "flac_io: read %lx * %lx to %p\n", size, nmemb, ptr);
	ssize_t read_size;
	struct buffer *b;

	b = (struct buffer *) handle;

	read_size = size * nmemb;

	memcpy(ptr, b->data, read_size);
	
	b->data += read_size;
	b->rpos += read_size;

	return read_size;
}

static int
flac_io_seek(FLAC__IOHandle handle, FLAC__int64 offset, int whence) {

	struct buffer *b;

	b = (struct buffer *) handle;

	switch(whence) {
	case SEEK_SET:
		fprintf(stderr, "flac_io: seek set to %lx\n", offset);
		if (offset < 0)
			return -1;
		if (offset > b->size)
			return -1;

		b->rpos = offset;
		b->rptr = b->rptr + offset;
		break;
	case SEEK_END:
		fprintf(stderr, "flac_io: seek end to %lx\n", offset);
		b->rpos = b->size;
		b->rptr = &(b->data[b->size]);
	case SEEK_CUR:
		fprintf(stderr, "flac_io: seek cur to %lx\n", offset);
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
flac_io_tell(FLAC__IOHandle handle) {
	struct buffer *b;

	b = (struct buffer *) handle;

	fprintf(stderr, "flac_io: telling rpos %lx rptr %p\n", b->rpos, b->rptr);

	return b->rpos;
}

void
flac_metadata_vorbis_comment_dump(FLAC__StreamMetadata *metablock)
{
	FLAC__uint32 i;

	for (i = 0; i < metablock->data.vorbis_comment.num_comments; i++) {
		fprintf(stderr, "flac: vorbis_comment %s\n", (char *) 
		    metablock->data.vorbis_comment.comments[i].entry);
         }
}

void 
flac_test(struct buffer *b)
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

	rv = FLAC__metadata_chain_read_with_callbacks(chain, (FLAC__IOHandle)b, iocb);

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

