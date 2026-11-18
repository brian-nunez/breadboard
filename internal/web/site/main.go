// Command site writes the docs, the starters and the landing page of the
// web UI as a static site, for breadboard.b8z.me.
//
//	go run ./internal/web/site <outdir>
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/brian-nunez/breadboard/internal/core"
	"github.com/brian-nunez/breadboard/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "site: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: go run ./internal/web/site <outdir>")
	}

	eng, err := core.New(core.Options{})
	if err != nil {
		return err
	}

	return web.Export(context.Background(), eng, args[0])
}
