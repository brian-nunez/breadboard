package cli

import (
	"flag"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/brian-nunez/breadboard/internal/web"
)

// openBrowser opens a URL in the default browser. Tests replace it.
var openBrowser = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}

	return exec.Command(name, url).Start()
}

func (c *cli) serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "loopback address to listen on")
	noOpen := fs.Bool("no-open", false, "print the URL without opening a browser")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if err := exactly(pos, 0, ""); err != nil {
		return err
	}

	srv, err := web.New(c.eng, web.Options{Dir: c.dir, Addr: *addr})
	if err != nil {
		return fmt.Errorf("%w (choose another with --addr 127.0.0.1:<port>)", err)
	}

	_, _ = fmt.Fprintf(c.out, "Breadboard is serving a read-only view of this experiment at\n  %s\n", srv.URL())
	_, _ = fmt.Fprintln(c.out, "Open that link (it signs this browser in); press Ctrl+C to stop.")

	if !*noOpen {
		if err := openBrowser(srv.URL()); err != nil {
			_, _ = fmt.Fprintf(c.errOut, "bb: could not open a browser: %v\n", err)
		}
	}
	// Every page and live view asks for status; one shared stats stream
	// spares each a two-second docker stats sample.
	// stop waits for the docker stats process to end, so bb never exits
	// leaving it running.
	stop := c.eng.StartStatsSampler(c.ctx)
	defer stop()

	return srv.Serve(c.ctx)
}
