package cli

import (
	"flag"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/mcp"
)

// mcpTransport is where bb mcp speaks the protocol: stdin and stdout. Tests
// replace it.
var mcpTransport = func() sdk.Transport { return &sdk.StdioTransport{} }

// mcpCmd serves the core as MCP tools over stdio. stdout belongs to the
// protocol, so nothing else is printed there.
func (c *cli) mcpCmd(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	readOnly := fs.Bool("read-only", false, "expose only the tools that change nothing you own")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 0, ""); err != nil {
		return err
	}
	srv, err := mcp.New(c.eng, mcp.Options{Dir: c.dir, ReadOnly: *readOnly})
	if err != nil {
		return err
	}
	return srv.Run(c.ctx, mcpTransport())
}
