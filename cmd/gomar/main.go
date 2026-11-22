package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mbaragiola/gomar/internal/preview"
	"github.com/mbaragiola/gomar/internal/whatsapppreview"
)

const usage = "usage: gomar --synthetic-demo | --synthetic-claude | --synthetic-whatsapp"

func run(ctx context.Context, args []string, in io.ReadCloser, out io.WriteCloser) error {
	if len(args) != 1 {
		return errors.New(usage)
	}
	switch args[0] {
	case "--synthetic-demo":
		return preview.Serve(ctx, in, out)
	case "--synthetic-claude":
		return preview.ServeClaude(ctx, out)
	case "--synthetic-whatsapp":
		return whatsapppreview.Replay(ctx, in, out)
	default:
		return errors.New(usage)
	}
}

func main() {
	ctx := context.Background()
	if len(os.Args) == 2 && os.Args[1] == "--synthetic-claude" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
