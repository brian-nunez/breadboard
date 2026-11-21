package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	readTools  = []string{"ca", "catalog", "config_get", "consul_catalog", "creds", "envoy_clusters", "guide", "haproxy_stats", "kafka_topics", "learn", "logs", "nginx_status", "otel_telemetry", "pgbouncer_pools", "postgres_tables", "redis_keys", "s3_buckets", "service_details", "starter_details", "starters", "status", "traefik_routes", "version"}
	writeTools = []string{"add_app", "add_service", "apply_starter", "config_set", "down", "export_compose", "init", "migrate", "remove_service", "up"}
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

// status and service_details describe sidecars and replicas; the sidecar
// explorers return each upstream host with the replica at its address.
func TestSidecarsAndReplicasAreVisible(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})

	spec := `version: 1
name: lab
services:
  postgres:
    type: postgres
apps:
  web:
    path: ./web
    command: [web]
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  caller:
    path: ./caller
    command: [caller]
    uses: [postgres]
    sidecars:
      - type: envoy
        outbound: [{service: web, port: 9001}]
        transparent: {egress: deny}
      - type: pgbouncer
        service: postgres
`
	if err := os.WriteFile(filepath.Join(h.root, "experiment.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	st := h.mustCall(t, "status", nil)

	rows := map[string]map[string]any{}
	for _, s := range st["services"].([]any) {
		rows[s.(map[string]any)["name"].(string)] = s.(map[string]any)
	}

	if w := rows["web-2"]; w["app"] != "web" || w["instance"] != 2.0 || w["replicas"] != 3.0 {
		t.Errorf("web-2 = %v", w)
	}

	ce, _ := rows["caller-envoy"]["sidecar"].(map[string]any)
	if ce == nil || ce["type"] != "envoy" || ce["transparent"] != true || ce["egress"] != "deny" || len(ce["ports"].([]any)) < 4 {
		t.Errorf("caller-envoy sidecar = %v", ce)
	}

	uses := rows["caller"]["uses"].([]any)
	if e := uses[1].(map[string]any); e["to"] != "web" || e["via"] != "envoy" || e["to_app"] != true {
		t.Errorf("caller uses = %v", uses)
	}

	d := h.mustCall(t, "service_details", map[string]any{"service": "caller"})
	if len(d["sidecars"].([]any)) != 2 || len(d["replicas"].([]any)) != 1 {
		t.Errorf("caller details = %v", d)
	}

	pb := d["sidecars"].([]any)[1].(map[string]any)
	for _, v := range pb["rewrites"].([]any) {
		if v := v.(map[string]any); v["secret"] == true && v["value"] != "" {
			t.Errorf("service_details shows %s", v["name"])
		}
	}

	data, err := os.ReadFile(filepath.Join("..", "core", "testdata", "explore", "envoy-outbound.txt"))
	if err != nil {
		t.Fatal(err)
	}

	h.rt.mu.Lock()
	h.rt.exec = map[string][]byte{"caller-envoy": data}
	h.rt.mu.Unlock()
	out := h.mustCall(t, "envoy_clusters", map[string]any{"service": "caller"})

	c := out["clusters"].([]any)[0].(map[string]any)
	if out["sidecar"] != "caller-envoy" || c["target"] != "web" || len(c["hosts"].([]any)) != 3 {
		t.Fatalf("envoy_clusters = %v", out)
	}

	if h := c["hosts"].([]any)[2].(map[string]any); h["instance"] != "web-3" || h["errors"] != 15.0 {
		t.Errorf("web-3 host = %v", h)
	}

	if _, errText := h.call(t, "pgbouncer_pools", map[string]any{"service": "web"}); !strings.Contains(errText, "no pgbouncer sidecar") {
		t.Errorf("pgbouncer_pools on an app without one: %q", errText)
	}
}
