// gomar-mcp serves the Gomar chat tools over local stdio.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mbaragiola/gomar/app/internal/gomarmcp"
	"github.com/mbaragiola/gomar/app/internal/payablechat"
)

func run(args []string, in io.Reader, out io.Writer) error {
	const prefix = "--synthetic-scope="
	if len(args) > 0 && !strings.HasPrefix(args[0], "--synthetic-") {
		return runLive(context.Background(), args, in, out)
	}
	if len(args) != 1 || !strings.HasPrefix(args[0], prefix) {
		return errors.New("usage: gomar-mcp --synthetic-scope=centro|norte")
	}
	scope := strings.TrimPrefix(args[0], prefix)
	reader, err := payablechat.NewSynthetic(scope)
	if err != nil {
		return err
	}
	return gomarmcp.Serve(context.Background(), in, out, reader)
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
