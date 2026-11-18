// Package cli is the command-line wrapper over the core. Each command parses
// its flags, calls one core operation, and renders the result. It holds no
// domain logic.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

type command struct {
	name    string
	args    string
	summary string
	run     func(c *cli, args []string) error
}

type cli struct {
	ctx    context.Context
	eng    *core.Engine
	out    io.Writer
	errOut io.Writer
	dir    string
}

var commands []command

func init() {
	commands = []command{
		{"init", "[name]", "Create experiment.yaml and .bb/ here; create BB_HOME and the CA if missing", (*cli).initCmd},
		{"catalog", "", "List infra service types", (*cli).catalog},
		{"add", "<type> [--name n] | app <name> [--uses a,b] [--dir path]", "Add an infra service, or write your own app and add it, to experiment.yaml", (*cli).add},
		{"remove", "<name>", "Remove a service or app from experiment.yaml", (*cli).remove},
		{"starters", "", "List starters", (*cli).starters},
		{"starter", "<id> [--dir path]", "Write a starter's code and add its apps", (*cli).starter},
		{"up", "[--timeout d]", "Start the experiment and wait for health", (*cli).up},
		{"down", "[--keep-volumes]", "Stop the experiment and delete its data volumes; --keep-volumes keeps them", (*cli).down},
		{"status", "", "Show each service's state, health, CPU, and memory", (*cli).status},
		{"logs", "[service] [-f] [--tail n]", "Show or follow logs", (*cli).logs},
		{"creds", "[service]", "Show generated credentials and connection strings", (*cli).creds},
		{"learn", "<type>", "Show a service type's learning notes", (*cli).learn},
		{"ca", "[--host] [--pem]", "Show the Breadboard CAs and how to trust them", (*cli).ca},
		{"config", "[get <key> | set <key> <value>]", "Show or change install settings", (*cli).config},
		{"migrate", "", "Upgrade experiment.yaml to the current spec version", (*cli).migrate},
		{"export", "compose [--out dir] [--secrets inline|dotenv]", "Write a standalone compose stack", (*cli).export},
		{"serve", "[--addr host:port] [--no-open]", "Open the read-only web UI for this experiment", (*cli).serve},
		{"mcp", "[--read-only]", "Serve bb as MCP tools over stdio, for AI agents", (*cli).mcpCmd},
		{"version", "", "Show the bb version", (*cli).version},
		{"help", "[command]", "Show help", (*cli).help},
	}
}

// Run executes bb with args (without the program name) and returns the exit code.
func Run(ctx context.Context, eng *core.Engine, args []string, stdout, stderr io.Writer) int {
	c := &cli{ctx: ctx, eng: eng, out: stdout, errOut: stderr}
	global := flag.NewFlagSet("bb", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&c.dir, "C", "", "run as if bb was started in `dir`")

	if err := global.Parse(args); err != nil {
		_, _ = fmt.Fprintf(stderr, "bb: %v\n\n", err)
		c.usage(stderr)

		return exitUsage
	}

	args = global.Args()
	if len(args) == 0 {
		c.usage(stdout)

		return exitOK
	}

	name, rest := args[0], args[1:]
	if name == "-h" || name == "--help" {
		c.usage(stdout)

		return exitOK
	}

	for _, cmd := range commands {
		if cmd.name != name {
			continue
		}

		err := cmd.run(c, rest)
		if err == nil {
			return exitOK
		}

		var ue usageError
		if errors.As(err, &ue) {
			_, _ = fmt.Fprintf(stderr, "bb %s: %s\nusage: bb %s %s\n", cmd.name, ue.msg, cmd.name, cmd.args)

			return exitUsage
		}

		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stdout, "usage: bb %s %s\n\n%s\n", cmd.name, cmd.args, cmd.summary)

			return exitOK
		}

		c.printError(err)

		return exitError
	}

	_, _ = fmt.Fprintf(stderr, "bb: unknown command %q\n\n", name)
	c.usage(stderr)

	return exitUsage
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

func (c *cli) usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "bb (Breadboard): a local lab for real system design.")
	_, _ = fmt.Fprintln(w, "\nusage: bb [-C dir] <command> [arguments]")
	_, _ = fmt.Fprintln(w, "\ncommands:")

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, cmd := range commands {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", cmd.name, cmd.summary)
	}

	_ = tw.Flush()
	_, _ = fmt.Fprintln(w, "\nRun bb help <command> for its arguments.")
}

func (c *cli) help(args []string) error {
	if len(args) == 0 {
		c.usage(c.out)

		return nil
	}

	for _, cmd := range commands {
		if cmd.name == args[0] {
			_, _ = fmt.Fprintf(c.out, "usage: bb %s %s\n\n%s\n", cmd.name, cmd.args, cmd.summary)

			return nil
		}
	}

	return usagef("unknown command %q", args[0])
}

func (c *cli) printError(err error) {
	var ve *core.ValidationError
	if errors.As(err, &ve) {
		_, _ = fmt.Fprintln(c.errOut, "bb: experiment.yaml is invalid:")

		for _, v := range ve.Violations {
			_, _ = fmt.Fprintf(c.errOut, "  %s\n", v)
		}

		return
	}

	_, _ = fmt.Fprintf(c.errOut, "bb: %v\n", err)

	switch {
	case errors.Is(err, core.ErrDockerUnavailable):
		_, _ = fmt.Fprintln(c.errOut, "Start Docker (Docker Desktop, OrbStack, or the docker daemon) and try again.")
	case errors.Is(err, core.ErrBusy):
		_, _ = fmt.Fprintln(c.errOut, "Wait for it to finish, then try again.")
	case errors.Is(err, core.ErrNewerFormat):
		_, _ = fmt.Fprintln(c.errOut, "Upgrade bb: go install github.com/brian-nunez/breadboard/bb@latest")
	}
}

func newFlagSet(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }

// parse parses flags that may appear before, between, or after positional
// arguments, as in `bb logs consumer -f`.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)

	var positional []string

	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}

			return nil, usagef("%v", err)
		}

		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}

		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}

		positional = append(positional, args[0])
		args = args[1:]
	}
}

func exactly(args []string, n int, what string) error {
	if len(args) != n {
		if n == 0 {
			return usagef("unexpected argument %q", args[0])
		}

		return usagef("expected %s", what)
	}

	return nil
}

func atMost(args []string, n int) error {
	if len(args) > n {
		return usagef("unexpected argument %q", args[n])
	}

	return nil
}

func (c *cli) table() *tabwriter.Writer {
	return tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
}

// events renders core progress events.
func (c *cli) events() core.EventSink {
	return func(ev core.Event) {
		switch ev.Kind {
		case core.EventStep:
			_, _ = fmt.Fprintf(c.errOut, "==> %s\n", ev.Message)
		case core.EventHealth:
			_, _ = fmt.Fprintf(c.errOut, "    %-14s %s\n", ev.Service, ev.Health)
		case core.EventWarning:
			_, _ = fmt.Fprintf(c.errOut, "warning: %s\n", ev.Message)
		default:
			_, _ = fmt.Fprintf(c.errOut, "    %s\n", ev.Message)
		}
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}

func join(items []string) string {
	if len(items) == 0 {
		return "-"
	}

	return strings.Join(items, ", ")
}
