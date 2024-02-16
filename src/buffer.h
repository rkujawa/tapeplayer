#ifndef _BUFFER_H_
#define _BUFFER_H_

struct buffer {
	size_t size;	/*< size in bytes */
	size_t used;	/*< currently used space in the buffer, in bytes */
	size_t blocks;  /*< amount of blocks in the buffer */

	size_t rpos;	/*< only used when reading, to tell the current position */
	uint8_t *rptr;

	uint8_t *data;
};

struct buffer buffer_init(size_t size);
void buffer_realloc(struct buffer *b);


#define BUFFER_SIZE_DEFAULT 10*1024*1024


#endif /* _BUFFER_H_ */
