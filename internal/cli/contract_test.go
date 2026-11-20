package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCLIContractCommands locks the exact command surface, help output, and
// usage exit codes of `bb`.
func TestCLIContractCommands(t *testing.T) {
	wantCommands := []string{
		"init", "catalog", "add", "remove", "starters", "starter",
		"up", "down", "status", "logs", "creds", "learn", "ca",
		"config", "migrate", "export", "serve", "mcp", "version", "help",
	}

	got := make([]string, len(commands))
	for i, cmd := range commands {
		got[i] = cmd.name
		if strings.TrimSpace(cmd.summary) == "" {
			t.Errorf("command %q has empty summary", cmd.name)
		}
	}

	if !slices.Equal(got, wantCommands) {
		t.Fatalf("command list drifted:\n got: %v\nwant: %v", got, wantCommands)
	}

	eng := engine(t)

	// No args and `help` exit 0 and print top-level usage on stdout.
	for _, args := range [][]string{{}, {"help"}} {
		code, out, errOut := run(t, eng, args...)
		if code != exitOK {
			t.Errorf("args %v: exit = %d, want %d (stderr=%q)", args, code, exitOK, errOut)
		}

		if !strings.Contains(out, "usage: bb [-C dir] <command> [arguments]") {
			t.Errorf("args %v: missing usage banner in %q", args, out)
		}
	}

	// Top-level -h/--help currently triggers global flag.ErrHelp (exit 2, usage on stderr).
	for _, args := range [][]string{{"-h"}, {"--help"}} {
		code, _, errOut := run(t, eng, args...)
		if code != exitUsage || !strings.Contains(errOut, "usage: bb [-C dir] <command> [arguments]") {
			t.Errorf("args %v: exit=%d stderr=%q", args, code, errOut)
		}
	}

	// Every registered command answers `bb help <cmd>` and `bb <cmd> --help` with exit 0.
	for _, name := range wantCommands {
		code, out, errOut := run(t, eng, "help", name)
		if code != exitOK || !strings.Contains(out, "usage: bb "+name) {
			t.Errorf("bb help %s: exit=%d out=%q err=%q", name, code, out, errOut)
		}

		if name == "help" {
			continue
		}

		code, out, errOut = run(t, eng, name, "--help")
		if code != exitOK || !strings.Contains(out, "usage: bb "+name) {
			t.Errorf("bb %s --help: exit=%d out=%q err=%q", name, code, out, errOut)
		}
	}
}

// TestCLIContractExitCodes locks exit-code separation: 0 success, 2 usage error,
// 1 domain/validation/runtime error.
func TestCLIContractExitCodes(t *testing.T) {
	eng := engine(t)
	emptyDir := t.TempDir()

	usageCases := [][]string{
		{"--unknown-global-flag"},
		{"not-a-command"},
		{"help", "not-a-command"},
		{"remove"},
		{"learn"},
		{"add"},
		{"add", "--name", "shop", "app"},
		{"down", "--keep-volumes", "--volumes"},
		{"export"},
		{"export", "unknown-format"},
	}

	for _, args := range usageCases {
		code, _, errOut := run(t, eng, args...)
		if code != exitUsage {
			t.Errorf("usage case %v: exit = %d, want %d (stderr=%q)", args, code, exitUsage, errOut)
		}
	}

	// Domain error on an uninitialized directory exits 1, not 2.
	for _, args := range [][]string{
		{"-C", emptyDir, "creds"},
		{"-C", emptyDir, "remove", "postgres"},
		{"-C", emptyDir, "export", "compose"},
	} {
		code, _, errOut := run(t, eng, args...)
		if code != exitError {
			t.Errorf("domain case %v: exit = %d, want %d (stderr=%q)", args, code, exitError, errOut)
		}
	}
}

// TestCLIContractEndToEndFlow locks the end-to-end CLI contract across init,
// add, add app, creds, and export compose (dotenv + inline).
func TestCLIContractEndToEndFlow(t *testing.T) {
	eng := engine(t)

	dir := filepath.Join(t.TempDir(), "contract-lab")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"-C", dir, "init", "contract-lab"}, "Created experiment contract-lab"},
		{[]string{"-C", dir, "add", "postgres"}, "Added postgres"},
		{[]string{"-C", dir, "add", "redis"}, "Added redis"},
		{[]string{"-C", dir, "add", "app", "worker"}, "Added app worker"},
		{[]string{"-C", dir, "creds"}, "DATABASE_URL"},
	} {
		code, out, errOut := run(t, eng, step.args...)
		if code != exitOK || !strings.Contains(out, step.want) {
			t.Fatalf("step %v: code=%d want=%q\nout=%s\nerr=%s", step.args, code, step.want, out, errOut)
		}
	}

	// Export with dotenv secrets must write .env and never inline the postgres password in docker-compose.yml.
	exportDir := filepath.Join(t.TempDir(), "out-dotenv")

	code, out, errOut := run(t, eng, "-C", dir, "export", "compose", "--out", exportDir, "--secrets", "dotenv")
	if code != exitOK {
		t.Fatalf("export compose dotenv: code=%d out=%q err=%q", code, out, errOut)
	}

	for _, rel := range []string{"docker-compose.yml", ".env", "README.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(exportDir, rel)); err != nil {
			t.Errorf("export compose dotenv missing %s: %v", rel, err)
		}
	}

	envBytes, err := os.ReadFile(filepath.Join(exportDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}

	composeBytes, err := os.ReadFile(filepath.Join(exportDir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}

	for line := range strings.SplitSeq(string(envBytes), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		_, val, ok := strings.Cut(line, "=")
		if ok && len(val) >= 8 && strings.Contains(string(composeBytes), val) {
			t.Errorf("dotenv secret leaked into docker-compose.yml")
		}
	}
}
