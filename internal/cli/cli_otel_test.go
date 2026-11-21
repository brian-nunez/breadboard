package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAddServiceWithConfig(t *testing.T) {
	eng := engine(t)
	dir := filepath.Join(t.TempDir(), "otel-cli")

	code, _, errOut := run(t, eng, "-C", dir, "init", "otel-cli")
	if code != exitOK {
		t.Fatalf("init failed: %d %s", code, errOut)
	}

	code, out, errOut := run(t, eng, "-C", dir, "add", "otel", "--config", "signals=traces,logs")
	if code != exitOK || !strings.Contains(out, "Added otel") {
		t.Fatalf("add otel --config: code=%d out=%q err=%q", code, out, errOut)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "signals: traces,logs") {
		t.Fatalf("experiment.yaml missing signals:\n%s", raw)
	}

	// Malformed --config returns usage error (exit 2).
	code, _, _ = run(t, eng, "-C", dir, "add", "otel", "--name", "otel2", "--config", "missing-equals")
	if code != exitUsage {
		t.Fatalf("malformed --config exit = %d, want %d", code, exitUsage)
	}
}
