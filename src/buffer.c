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

	fprintf(stderr, "Buffer: reallocation (current size: %lx, new size: %lx, used: %lx, blocks: %lx\n", b->size, new_size, b->used, b->blocks);

	b->data = GC_realloc(b->data, new_size);
	b->size = new_size;
}
