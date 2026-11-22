package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mbaragiola/gomar/internal/preview"
	"github.com/mbaragiola/gomar/internal/whatsapppreview"
)

func run(ctx context.Context, args []string, in io.ReadCloser, out io.WriteCloser) error {
	if len(args) == 1 {
		switch args[0] {
		case "--synthetic-demo":
			return preview.Serve(ctx, in, out)
		case "--synthetic-whatsapp":
			return whatsapppreview.Replay(ctx, in, out)
		}
	}
	return errors.New("usage: gomar --synthetic-demo | --synthetic-whatsapp")
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
