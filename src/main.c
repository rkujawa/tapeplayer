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

#include <sys/mtio.h>

#include <gc/gc.h>
#include <utlist.h>

#include "buffer.h"
#include "tapeio.h"
#include "flac.h"

bool f_verbose = false;

static void
usage(const char *exec_name)
{
	fprintf(stderr, "%s [-f /dev/name] [-v]\n", exec_name);
}

int
main(int argc, char *argv[])
{
	const char *exec_name;
	const char *dev_name;

	struct tape_device td;
	struct tape_reader *tr;

	int flag = 0;

	GC_INIT();

//	GC_allow_register_threads();

	exec_name = GC_STRDUP(argv[0]);

	if ((dev_name = getenv("TAPE")) == NULL)
//		dev_name = GC_STRDUP(default_tape);
		dev_name = GC_STRDUP(TAPE_DEFAULT_DEVICE);

	while ((flag = getopt(argc, argv, "f:v")) != -1) {
		switch (flag) {
			case 'f':
				dev_name = GC_STRDUP(optarg);
				break;
			case 'v':
				f_verbose = 1;
				break;
		}
	}

	argv += optind;
	argc -= optind;

/*	if (f_verbose)
		uscsilib_verbose = 1;*/
//	b = buffer_init(BUFFER_SIZE_DEFAULT);

//	td = tape_device_open(dev_name, false);
//
//	if (ioctl(td.fd, MTIOCGET, &mt_status) < 0)
//		err(EXIT_FAILURE, "Failed to read device status");
//
//	printf("mt_dsreg: 0x%lx, mt_gstat: 0x%lx\n", mt_status.mt_dsreg, mt_status.mt_gstat);

	// should include some callback instead of blocking
	tape_poll_for_readiness(dev_name);

//	td = tape_device_reopen_blocking(&td);
	td = tape_device_open(dev_name, true);

	tape_status_dump(&td);
	tape_rewind(&td);
	tape_status_dump(&td);

//	tr = tape_reader_start(&td, tape_read_file_to_buffer);
	tr = tape_reader_start(&td, tape_read_file_head_to_buffer);
	printf("buf usage: %zd \n", tr->b.used);
/*	tape_read_block_to_buffer(&td, &b);
	tape_read_block_to_buffer(&td, &b);
	tape_read_block_to_buffer(&td, &b);*/

//	sleep(20);
//	printf("buf usage: %zd \n", tr->b.used);

	tape_reader_wait();
	printf("buf usage: %zd \n", tr->b.used);

	flac_test(&(tr->b));

	tape_block_tell(&td);

	tape_device_close(&td);

	return EXIT_SUCCESS;

}

