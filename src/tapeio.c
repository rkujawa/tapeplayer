#define _GNU_SOURCE

#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <stdbool.h>
#include <errno.h>
#include <unistd.h>
#include <string.h>
#include <fcntl.h>
#include <err.h>

#include <pthread.h>

#include <sys/ioctl.h>
#include <sys/mtio.h>
#include <sys/time.h>

#include <gc/gc.h>
#include <utlist.h>

#include "tapeio.h"

#define BLOCK_SIZE_DEFAULT 2048

extern bool f_verbose;

static struct tape_reader tr; /* there can be only one */

struct tape_device
tape_device_open(const char *dev_name, bool blocking) {

	struct tape_device td;
	int o_flags = O_RDONLY;

	td.blocking = true;
	td.dev_name = GC_STRDUP(dev_name);

	if (f_verbose)
		printf("Opening device %s\n", dev_name);

	if (blocking == false) {
		o_flags |= O_NONBLOCK;
		td.blocking = false;
	}

	if ((td.fd = open(dev_name, o_flags)) < 0) { 
		td.err_msg = GC_STRDUP(strerror(errno));
		td.open = false;
		fprintf(stderr, "Failed to open device: %s\n", strerror(errno));
	} else {
		td.err_msg = NULL;
		td.open = true;
	}

	return td;
}

void
tape_device_close(struct tape_device *td)
{
	int rv;
	
	rv = 0;

	if (td->open)
		rv = close(td->fd);
	// else warn that tape is already closed

	assert(rv == 0);
}

struct tape_device
tape_device_reopen_blocking(struct tape_device *td)
{
	assert(td != NULL);
	assert(td->open);
	assert(!(td->blocking));

	tape_device_close(td);

	return tape_device_open(td->dev_name, true);
}

ssize_t
tape_read_block_to_buffer(struct tape_device *td, struct buffer *b)
{
	ssize_t rb;

	assert(td != NULL);
	assert(td->open);
	assert(td->blocking);

	if (b->used + BLOCK_SIZE_DEFAULT > b->size) {
		buffer_realloc(b);
	}

	rb = read(td->fd, b->data + b->used, BLOCK_SIZE_DEFAULT);
	b->used += rb;
	(b->blocks)++;

	if (rb < 0) {
		buffer_state_dump(b);
		perror("Error reading block from tape");
	}

	/* if no space in buffer, realloc buffer */

	return rb;
}

void
tape_read_thread_init()
{
/*	GC_get_stack_base(&(tr.sb));
	GC_register_my_thread(&(tr.sb));*/

	// if verbose
	fprintf(stderr, "tape_reader: started tape_reader thread\n");
#ifdef _GNU_SOURCE
	pthread_setname_np(tr.thread, "tape_reader");
#endif
	tr.ts = READING;

	gettimeofday(&tr.start, NULL);	
	// run some callback to update UI that we are starting to read
}

void
tape_read_thread_finish()
{
	//GC_unregister_my_thread();
	//if verbose
    	fprintf(stderr, "tape_reader: finished thread\n");

	tr.ts = STOPPED;
	// run some callback to updte UI that we stopped reading
}


/*
 * Read current tape file to buffer (in a thread).
 */
void *
tape_read_file_to_buffer(void *arg)
{
	ssize_t rb;

	tape_read_thread_init();

	tr.b = buffer_init(BUFFER_SIZE_DEFAULT);

	while ((rb = tape_read_block_to_buffer(tr.td, &(tr.b))) > 0) {
		//if (f_verbose)
//		fprintf(stderr, "Read block %lx, buffer position %lx\n", tr.b.blocks, tr.b.used);
		;;
	}

	tape_read_thread_finish();
	
	return NULL;
} 


// read ~20MB since flac metadata is limited to 2^24 (16MB).
void *
tape_read_file_head_to_buffer(void *arg)
{
	ssize_t rb;

	tape_read_thread_init();

	tr.b = buffer_init(BUFFER_SIZE_DEFAULT);

	while ((rb = tape_read_block_to_buffer(tr.td, &(tr.b))) > 0) {
		//if (f_verbose)
//		fprintf(stderr, "Read block %lx, buffer position %lx\n", tr.b.blocks, tr.b.used);
		//if (tr.b.used >= 20*1024*1024)
		if (tr.b.used >= 1*1024*1024)
			break;
		;;
	}

	tape_read_thread_finish();
	
	return NULL;
} 

uint64_t
tape_bandwidth_get()
{
	struct timeval now;
	int64_t mS;
	uint64_t bandwidth;

	if ((tr.ts == STOPPED) || (tr.ts == OFFLINE))
		return 0;

	gettimeofday(&now, NULL);

#define	tv2mS(tv) ((tv).tv_sec * 1000LL + ((tv).tv_usec + 500) / 1000)
	mS = tv2mS(now) - tv2mS(tr.start);
	if (mS == 0)
		mS = 1;

	bandwidth = (tr.b.used * 1000LL / mS);
	fprintf(stderr, "bandwidth %lu/s\n", bandwidth);

	return bandwidth;
}

void
tape_reader_wait(void)
{
	pthread_join(tr.thread, NULL);
}

void
tape_reader_stop()
{
	// change state
	// stop
}

struct tape_reader *
tape_reader_start(struct tape_device *td, void *(*reader_thread_func)(void *)) {

	int rv;

	tr.td = td;

	// XXX check reader state and/or if the old thread is still running

	// http://www.cs.put.poznan.pl/ksiek/sk2/pthreads.html
	rv = pthread_create(&(tr.thread), NULL, reader_thread_func, NULL); //(void *) &tr);
										 //
	assert(rv == 0);

	return &tr;

}

void
//tape_poll_for_readiness(struct tape_device *td)
tape_poll_for_readiness(const char *dev_name)
{
	struct tape_device td;
	struct mtget mt_status;

	td.online = false;

	do {
		td = tape_device_open(dev_name, false);
		if (ioctl(td.fd, MTIOCGET, &mt_status) < 0)
			err(EXIT_FAILURE, "Failed to read device status");
		//if (f_verbose)
			fprintf(stderr, "Polling for tape readiness... (mt_gstat: %lx)\n", 
			    mt_status.mt_gstat);
	       
		sleep(1);
		tape_device_close(&td);
//	while (GMT_DR_OPEN(mt_status.mt_gstat) && !(GMT_ONLINE(mt_status.mt_gstat)));
	} while (!(GMT_ONLINE(mt_status.mt_gstat)));

	td.online = true;

}

void
tape_rewind(struct tape_device *td) 
{
	int rv;
	struct mtop mt_command;
	struct mtget mt_status;

	assert(td != NULL);

	rv = ioctl(td->fd, MTIOCGET, &mt_status);
	assert(rv == 0);

	assert((GMT_ONLINE(mt_status.mt_gstat)));

	if (GMT_BOT(mt_status.mt_gstat)) {
		fprintf(stderr, "tapeio: Tape already at BOT, not rewinding.\n");
		return;
	}

	mt_command.mt_op = MTREW;
	mt_command.mt_count = 1;

	rv = ioctl(td->fd, MTIOCTOP, (char *)&mt_command);

	assert(rv == 0);
}

void
tape_status_dump(struct tape_device *td)
{
	struct mtget mt_status;

	assert(td != NULL);

        if (ioctl(td->fd, MTIOCGET, &mt_status) < 0)
                err(EXIT_FAILURE, "Failed to read device status");

        fprintf(stderr, "tapeio: mt_dsreg: 0x%lx, mt_gstat: 0x%lx\n", mt_status.mt_dsreg, mt_status.mt_gstat);

}

size_t 
tape_block_tell(struct tape_device *td)
{
	struct mtpos mt_pos;
	int rv;

	rv = ioctl(td->fd, MTIOCPOS, (char *)&mt_pos);
	assert(rv == 0);

	// if verbose
	fprintf(stderr, "tape: now at block %lx\n", mt_pos.mt_blkno);

	return (size_t)mt_pos.mt_blkno;
}
