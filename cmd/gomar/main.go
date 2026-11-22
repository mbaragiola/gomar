package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mbaragiola/gomar/internal/preview"
)

func run(ctx context.Context, args []string, in io.ReadCloser, out io.WriteCloser) error {
	if len(args) != 1 || args[0] != "--synthetic-demo" {
		return errors.New("usage: gomar --synthetic-demo")
	}
	return preview.Serve(ctx, in, out)
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
