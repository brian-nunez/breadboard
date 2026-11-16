package mcp

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	readTools  = []string{"ca", "catalog", "config_get", "consul_catalog", "creds", "guide", "kafka_topics", "learn", "logs", "postgres_tables", "redis_keys", "s3_buckets", "service_details", "starter_details", "starters", "status", "version"}
	writeTools = []string{"add_service", "apply_starter", "config_set", "down", "export_compose", "init", "migrate", "remove_service", "up"}
)

func TestToolsMirrorTheCoreAndReadOnlyDropsWrites(t *testing.T) {
	all := slices.Sorted(slices.Values(append(slices.Clone(readTools), writeTools...)))
	got := slices.Sorted(func(yield func(string) bool) {
		for name := range newHarness(t, false).tools(t) {
			if !yield(name) {
				return
			}
		}
	})
	if !slices.Equal(got, all) {
		t.Errorf("tools = %v\nwant %v", got, all)
	}

	ro := newHarness(t, true).tools(t)
	for _, name := range readTools {
		if ro[name] == nil {
			t.Errorf("--read-only is missing %s", name)
		}
	}
	for _, name := range writeTools {
		if ro[name] != nil {
			t.Errorf("--read-only exposes %s", name)
		}
	}
}

func TestAnnotationsTellClientsWhatEachToolDoes(t *testing.T) {
	tools := newHarness(t, false).tools(t)
	for name, tool := range tools {
		a := tool.Annotations
		if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: want openWorldHint false (bb acts only on local Docker)", name)
			continue
		}
		destructive := a.DestructiveHint == nil || *a.DestructiveHint
		switch {
		case name == "down" || name == "remove_service":
			if a.ReadOnlyHint || !destructive {
				t.Errorf("%s: want destructive", name)
			}
		case slices.Contains(writeTools, name) || name == "ca" || name == "creds":
			if a.ReadOnlyHint || destructive {
				t.Errorf("%s: want a non-destructive write", name)
			}
		default:
			if !a.ReadOnlyHint {
				t.Errorf("%s: want readOnlyHint", name)
			}
		}
		if tool.Description == "" {
			t.Errorf("%s: no description", name)
		}
	}
}

func TestInspectAnExperiment(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})
	h.mustCall(t, "add_service", map[string]any{"type": "postgres"})

	st := h.mustCall(t, "status", nil)
	if st["experiment"] != "lab" || len(st["services"].([]any)) != 1 {
		t.Fatalf("status = %v", st)
	}

	// Credentials come back with their values: they are what an agent needs
	// to debug an app's connection.
	creds := h.mustCall(t, "creds", map[string]any{"service": "postgres"})
	env := creds["services"].([]any)[0].(map[string]any)["env"].([]any)
	var url string
	for _, v := range env {
		if v := v.(map[string]any); v["name"] == "DATABASE_URL" {
			url = v["value"].(string)
		}
	}
	if !regexp.MustCompile(`^postgres://app:[^@<>]{16,}@postgres\.bb\.internal:5432/`).MatchString(url) {
		t.Errorf("DATABASE_URL = %q, want it with the generated password", url)
	}

	// service_details still leaves secret values out; creds is where they are.
	d := h.mustCall(t, "service_details", map[string]any{"service": "postgres"})
	for _, g := range d["env"].([]any) {
		for _, v := range g.(map[string]any)["vars"].([]any) {
			if v := v.(map[string]any); v["secret"] == true && v["value"] != "" {
				t.Errorf("service_details shows %s", v["name"])
			}
		}
	}
}

func TestInspectErrorsReadForAModel(t *testing.T) {
	h := newHarness(t, false)
	if _, errText := h.call(t, "status", nil); !strings.Contains(errText, "call init") {
		t.Errorf("status before init: %q", errText)
	}
	if _, errText := h.call(t, "status", map[string]any{"dir": t.TempDir()}); !strings.Contains(errText, "outside") {
		t.Errorf("status outside the root: %q", errText)
	}
	if _, errText := h.call(t, "learn", map[string]any{}); errText == "" {
		t.Error("learn without a type: want an input validation error")
	}
}

func TestLogsCapTheTail(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})
	for tail, want := range map[int]int{0: defaultLogTail, 50: 50, 1_000_000: maxLogTail} {
		out := h.mustCall(t, "logs", map[string]any{"tail": tail})
		if h.rt.tail != want || int(out["tail"].(float64)) != want {
			t.Errorf("tail %d: asked Docker for %d, reported %v; want %d", tail, h.rt.tail, out["tail"], want)
		}
		if lines := out["lines"].([]any); len(lines) != 1 {
			t.Errorf("lines = %v", lines)
		}
	}
}

// Every tool the instructions and the guide name must exist, so a model is
// never sent after a tool that is not there.
func TestInstructionsNameRealTools(t *testing.T) {
	tools := newHarness(t, false).tools(t)
	known := func(name string) bool { return tools[name] != nil }
	quoted := regexp.MustCompile(`"([a-z_]+)"`)
	for _, m := range quoted.FindAllStringSubmatch(instructions, -1) {
		if m[1] != "dir" && !known(m[1]) {
			t.Errorf("instructions name %q", m[1])
		}
	}
	for _, line := range strings.Split(guide, "\n") {
		if !strings.HasPrefix(line, "1.") && !strings.HasPrefix(line, "2.") && !strings.HasPrefix(line, "3.") && !strings.HasPrefix(line, "4.") && !strings.HasPrefix(line, "5.") {
			continue
		}
		for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(line, -1) {
			if m[1] != "volumes" && !known(m[1]) {
				t.Errorf("guide workflow names %q", m[1])
			}
		}
	}
}
