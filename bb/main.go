// Command bb is Breadboard: a local lab for real system design.
//
//	go install github.com/brian-nunez/breadboard/bb@latest
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/brian-nunez/breadboard/internal/cli"
	"github.com/brian-nunez/breadboard/internal/core"
)

// version is set by release builds (-ldflags "-X main.version=vX.Y.Z").
// Empty, as after go install, the core reads the module version from the
// build info instead.
var version string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	eng, err := core.New(core.Options{Version: version})
	if err != nil {
		fmt.Fprintf(os.Stderr, "bb: %v\n", err)
		stop()
		os.Exit(1)
	}

	code := cli.Run(ctx, eng, os.Args[1:], os.Stdout, os.Stderr)

	stop()
	os.Exit(code)
}
