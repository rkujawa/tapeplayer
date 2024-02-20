#ifndef _TAPEIO_H_
#define _TAPEIO_H_

#include "buffer.h"

typedef enum {
	OFFLINE,
	STOPPED,
	READING
} tape_state;

struct tape_device {
	int fd;
	bool open; 
	bool blocking;
	bool online;
	const char *dev_name;
	const char *err_msg;
	/* block size? */
};

struct tape_reader {
	pthread_t thread;
	struct GC_stack_base sb;

	struct tape_device *td;
	struct buffer b;

	tape_state ts;

	struct timeval start;
};

struct tape_device tape_device_open(const char *dev_name, bool blocking);
void tape_device_close(struct tape_device *td);
struct tape_device tape_device_reopen_blocking(struct tape_device *td);
ssize_t tape_read_block_to_buffer(struct tape_device *td, struct buffer *b);
//void tape_poll_for_readiness(struct tape_device *td);
void tape_poll_for_readiness(const char *dev_name);

void *tape_read_file_to_buffer(void *arg);
void *tape_read_file_head_to_buffer(void *arg);
struct tape_reader * tape_reader_start(struct tape_device *td, void *(*reader_thread_func)(void *));
void tape_reader_wait();
void tape_status_dump(struct tape_device *td);
uint64_t tape_bandwidth_get();


void tape_rewind(struct tape_device *td);

#ifdef __linux__
#define TAPE_DEFAULT_DEVICE "/dev/nst0"
//const char *default_tape = "/dev/nst0";
#else
#define TAPE_DEFAULT_DEVICE "/dev/enrst0"
//const char *default_tape = "/dev/enrst0";
#endif

#endif /* _TAPEIO_H_ */
