package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAddServiceWithConfigFlag(t *testing.T) {
	eng := engine(t)
	dir := filepath.Join(t.TempDir(), "cfg-cli")

	code, _, errOut := run(t, eng, "-C", dir, "init", "cfg-cli")
	if code != exitOK {
		t.Fatalf("init failed: %d %s", code, errOut)
	}

	code, out, errOut := run(t, eng, "-C", dir, "add", "postgres", "--config", "database=orders", "--config", "user=shop")
	if code != exitOK || !strings.Contains(out, "Added postgres") {
		t.Fatalf("add postgres --config: code=%d out=%q err=%q", code, out, errOut)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "database: orders") || !strings.Contains(string(raw), "user: shop") {
		t.Fatalf("experiment.yaml missing config values:\n%s", raw)
	}

	// Malformed --config returns usage error (exit 2).
	code, _, _ = run(t, eng, "-C", dir, "add", "postgres", "--name", "pg2", "--config", "missing-equals")
	if code != exitUsage {
		t.Fatalf("malformed --config exit = %d, want %d", code, exitUsage)
	}

	// Unknown config key fails validation (exit 1).
	code, _, _ = run(t, eng, "-C", dir, "add", "postgres", "--name", "pg3", "--config", "nope=1")
	if code != exitError {
		t.Fatalf("unknown config key exit = %d, want %d", code, exitError)
	}
}
