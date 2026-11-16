package cli

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bb mcp serves on its transport and prints nothing on stdout, which belongs
// to the protocol; --read-only drops the tools that write.
func TestMCPServesToolsAndKeepsStdoutForTheProtocol(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		wantWrite bool
	}{{[]string{"mcp"}, true}, {[]string{"mcp", "--read-only"}, false}} {
		st, ct := sdk.NewInMemoryTransports()
		old := mcpTransport
		mcpTransport = func() sdk.Transport { return st }
		t.Cleanup(func() { mcpTransport = old })

		eng := engine(t)
		dir := t.TempDir()
		type result struct {
			code      int
			out, errs string
		}
		done := make(chan result, 1)
		go func() {
			code, out, errs := run(t, eng, append([]string{"-C", dir}, tc.args...)...)
			done <- result{code, out, errs}
		}()

		cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		hasUp := false
		for _, tool := range res.Tools {
			hasUp = hasUp || tool.Name == "up"
		}
		if hasUp != tc.wantWrite {
			t.Errorf("%v: up listed = %v, want %v", tc.args, hasUp, tc.wantWrite)
		}
		cs.Close()
		r := <-done
		if r.code != exitOK || r.out != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit 0 and nothing on stdout", tc.args, r.code, r.out, r.errs)
		}
	}
}

func TestMCPRejectsArguments(t *testing.T) {
	if code, _, errs := run(t, engine(t), "mcp", "extra"); code != exitUsage {
		t.Errorf("exit %d (%s), want usage", code, errs)
	}
}
