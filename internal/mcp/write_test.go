package mcp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDirMustStayInsideTheRoot(t *testing.T) {
	h := newHarness(t, false)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{outside, "..", "../elsewhere", "escape", "escape/lab"} {
		if _, errText := h.call(t, "init", map[string]any{"dir": dir}); !strings.Contains(errText, "outside") {
			t.Errorf("init in %q: want refusal, got %q", dir, errText)
		}
	}
	if _, errText := h.call(t, "export_compose", map[string]any{"out": outside}); !strings.Contains(errText, "outside") {
		t.Errorf("export_compose to %q: want refusal, got %q", outside, errText)
	}
	if _, err := os.Stat(filepath.Join(outside, "experiment.yaml")); err == nil {
		t.Fatal("an experiment was created outside the root")
	}

	out := h.mustCall(t, "init", map[string]any{"dir": "labs/one", "name": "one"})
	if want := filepath.Join(h.root, "labs", "one"); !strings.HasSuffix(out["dir"].(string), filepath.Join("labs", "one")) {
		t.Errorf("init dir = %v, want %s", out["dir"], want)
	}
	if _, err := os.Stat(filepath.Join(h.root, "labs", "one", "experiment.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestEditAnExperiment(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})
	if out := h.mustCall(t, "add_service", map[string]any{"type": "postgres"}); out["name"] != "postgres" {
		t.Fatalf("add_service = %v", out)
	}
	spec, err := os.ReadFile(filepath.Join(h.root, "experiment.yaml"))
	if err != nil || !strings.Contains(string(spec), "type: postgres") {
		t.Fatalf("experiment.yaml = %q, %v", spec, err)
	}

	if out := h.mustCall(t, "remove_service", map[string]any{"name": "postgres"}); out["kind"] != "service" {
		t.Errorf("remove_service = %v", out)
	}
}

func TestSpecErrorsReadForAModel(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})
	if _, errText := h.call(t, "add_service", map[string]any{"type": "mongodb"}); !strings.Contains(errText, "known: ") {
		t.Errorf("unknown type: %q", errText)
	}
	if err := os.WriteFile(filepath.Join(h.root, "experiment.yaml"), []byte("version: 1\nname: lab\nservices:\n  db:\n    type: postgres\n    colour: blue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errText := h.call(t, "add_service", map[string]any{"type": "redis"}); !strings.Contains(errText, "experiment.yaml is invalid") || !strings.Contains(errText, "services.db.colour") {
		t.Errorf("invalid spec: %q", errText)
	}
}

func TestLongOperationsReportProgress(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})
	h.mustCall(t, "add_service", map[string]any{"type": "redis"})
	out := h.mustCall(t, "export_compose", map[string]any{"out": "stack"})
	events := out["events"].([]any)
	if len(events) == 0 {
		t.Fatal("export_compose returned no events")
	}
	if _, err := os.Stat(filepath.Join(h.root, "stack", "docker-compose.yml")); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.progress) != len(events) {
		t.Errorf("progress notifications %v, want one per event (%d)", h.progress, len(events))
	}
}

// Cancelling a call cancels the core operation and releases the experiment's
// lock. With nothing running, up waits for health until it is cancelled.
func TestCancellingUpStopsIt(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})
	h.mustCall(t, "add_service", map[string]any{"type": "redis"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		params := &sdk.CallToolParams{Name: "up", Arguments: map[string]any{}}
		params.SetProgressToken("up")
		_, err := h.cs.CallTool(ctx, params)
		done <- err
	}()
	// Wait until up is waiting for health, then cancel.
	for deadline := time.Now().Add(10 * time.Second); ; {
		h.mu.Lock()
		waiting := slices.Contains(h.progress, "Waiting for health checks")
		h.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("up never started waiting for health")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled up returned no error")
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		_, errText := h.call(t, "add_service", map[string]any{"type": "postgres"})
		if errText == "" {
			break
		}
		if !strings.Contains(errText, "busy") || time.Now().After(deadline) {
			t.Fatalf("after cancelling up: %s", errText)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
