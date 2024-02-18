#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <stdbool.h>
#include <errno.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <err.h>
#include <assert.h>

#include <gc/gc.h>
#include <utlist.h>

#include "buffer.h"

struct buffer
buffer_init(size_t size)
{
	struct buffer b;

	b.data = GC_malloc(size);
	b.size = size;
	b.used = 0;
	b.blocks = 0;

	assert(b.data != NULL);

	return b;
}

void
buffer_realloc(struct buffer *b)
{
	ssize_t new_size;

	assert(b != NULL);
	assert(b->data != NULL);
	assert(b->used != 0);	/* we should not realloc when buffer empty */

	new_size = b->size + BUFFER_SIZE_DEFAULT;

	fprintf(stderr, "buffer: reallocation (current size: %zu, new size: %zu, used: %zu, blocks: %zu)\n", b->size, new_size, b->used, b->blocks);

	b->data = GC_realloc(b->data, new_size);
	b->size = new_size;
}

void
buffer_prefill_wait(struct buffer *b, size_t want)
{
	/* use a mutex here? */
	while(b->used < want)
		sleep(1);

	fprintf(stderr, "buffer: prefill condition met (used: %zu want: %zu)\n", b->used, want);
}

void
buffer_state_dump(struct buffer *b)
{
	fprintf(stderr, "buffer: size %zu, used %zu, blocks %zu, ptr %p",
	    b->size, b->used, b->blocks, b->data);
	if(b->rptr != NULL)
		fprintf(stderr, " rpos %zu, rptr %p", b->rpos, b->rptr);

	fprintf(stderr, "\n");
}
