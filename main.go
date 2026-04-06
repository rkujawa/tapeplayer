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
	debugFile := flag.String("debug", "", "debug log file (empty = no debug logging)")
	flag.Parse()

	if *portal == "" || *target == "" {
		fmt.Fprintf(os.Stderr, "error: -portal and -target are required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	// Logger: debug to file if specified, otherwise discard.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if *debugFile != "" {
		f, err := os.Create(*debugFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: open debug log: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		logger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Connect to iSCSI target.
	var opts []uiscsi.Option
	opts = append(opts, uiscsi.WithTarget(*target))
	opts = append(opts, uiscsi.WithLogger(logger))
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

	driveInfo := fmt.Sprintf("%s %s", drive.Info().VendorID, drive.Info().ProductID)

	// Create player.
	p := player.New(drive, logger)
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
