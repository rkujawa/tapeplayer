// Command tapeplayer plays FLAC audio files from iSCSI-attached tape drives.
//
// Usage:
//
//	tapeplayer -portal 192.168.1.100:3260 -target iqn.example:tape [-lun 0]
//
// The TUI launches in stopped state. Press space to start playback.
// Each FLAC file on tape is separated by a filemark; double filemark marks
// end of tape data.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/rkujawa/uiscsi"
	tape "github.com/rkujawa/uiscsi-tape"
	"github.com/rkujawa/tapeplayer/player"
	"github.com/rkujawa/tapeplayer/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	portal := flag.String("portal", "", "iSCSI target portal address (host:port)")
	target := flag.String("target", "", "target IQN")
	lun := flag.Uint64("lun", 0, "LUN number")
	initiatorName := flag.String("initiator-name", "", "initiator IQN (optional)")
	bs := flag.Uint("bs", 0, "fixed block size in bytes (0 = variable block mode)")
	decompress := flag.Bool("decompress", false, "enable hardware decompression (for tapes written with drive compression)")
	debugFile := flag.String("debug", "", "debug log file (empty = no debug logging)")
	flag.Parse()

	// Reduce GC frequency. The streamBuffer accumulates 400+ MB of live
	// data. With GOGC=100, pipeline allocations (~75 MB/s garbage) trigger
	// GC after ~6s, causing decode goroutine stalls. GOGC=400 delays the
	// first GC to ~24s of headroom and reduces subsequent GC frequency.
	debug.SetGCPercent(400)

	if *portal == "" || *target == "" {
		fmt.Fprintf(os.Stderr, "error: -portal and -target are required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	// Logger: debug to file if specified, otherwise discard.
	// Two loggers: one for tapeplayer (DEBUG), one for iSCSI session
	// (INFO). The session generates ~285 DEBUG entries/sec (per-PDU
	// logging) which causes I/O contention with the decode path.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sessLogger := logger
	if *debugFile != "" {
		f, err := os.Create(*debugFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: open debug log: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		logger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
		sessLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Second Ctrl+C force-quits without cleanup. This prevents unkillable
	// processes when the audio device is stuck in a kernel driver call.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh // first signal handled by NotifyContext above
		<-sigCh // second signal — force quit
		os.Exit(1)
	}()

	// Connect to iSCSI target.
	var opts []uiscsi.Option
	opts = append(opts, uiscsi.WithTarget(*target))
	opts = append(opts, uiscsi.WithLogger(sessLogger))
	opts = append(opts, uiscsi.WithMaxRecvDataSegmentLength(262144)) // 256KB PDUs for tape throughput
	if *initiatorName != "" {
		opts = append(opts, uiscsi.WithInitiatorName(*initiatorName))
	}

	sess, err := uiscsi.Dial(ctx, *portal, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: dial %s: %v\n", *portal, err)
		os.Exit(2)
	}
	defer sess.Close()

	// Open tape drive.
	var tapeOpts []tape.Option
	tapeOpts = append(tapeOpts, tape.WithLogger(logger))
	if *bs > 0 {
		tapeOpts = append(tapeOpts, tape.WithBlockSize(uint32(*bs)))
	} else {
		tapeOpts = append(tapeOpts, tape.WithSILI(true)) // suppress ILI for variable-block reads
	}

	drive, err := tape.Open(ctx, sess, *lun, tapeOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open tape LUN %d: %v\n", *lun, err)
		os.Exit(2)
	}
	defer drive.Close(ctx)

	// Enable hardware decompression if requested (default: yes).
	if *decompress {
		if err := drive.SetCompression(ctx, true, true); err != nil {
			// Not fatal — drive may not support compression page.
			logger.Warn("compression: could not enable", "err", err)
		}
	}

	// Rewind to BOT so file numbering is correct.
	if err := drive.Rewind(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: rewind: %v\n", err)
		os.Exit(2)
	}

	driveInfo := fmt.Sprintf("%s %s", drive.Info().VendorID, drive.Info().ProductID)

	// Read buffer: 4MB for multi-block reads. One SCSI READ(6) fetches
	// 8 blocks at 512KB each, reducing per-command overhead and keeping
	// the tape streaming continuously. For variable-block: 4MB covers
	// any reasonable record size.
	readBuf := 4 * 1024 * 1024
	if *bs > 0 && int(*bs) > readBuf {
		readBuf = int(*bs)
	}
	p := player.New(drive, logger, readBuf, 0) // 0 = default 500MB cache
	defer p.Close()

	// Create TUI.
	model := ui.New(p, ctx, driveInfo)
	prog := tea.NewProgram(model)

	// Connect player to TUI message bus.
	p.SetProgram(prog)

	// Run TUI (blocks until quit).
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
