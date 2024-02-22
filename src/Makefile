CFLAGS=-Wall -pedantic -g -O0 -DGC_THREADS -DGC_PTHREADS
LDFLAGS=-pthread -lgc -lFLAC -lao

all : tapeplayer

tapeplayer : main.o tapeio.o buffer.o flac.o audioio.o
	$(CC) $(LDFLAGS) -o $@ main.o tapeio.o buffer.o flac.o audioio.o

%.o : %.c *.h
	$(CC) $(CFLAGS) -c $<

clean :
	rm -f *.o
	rm -f mamtool

