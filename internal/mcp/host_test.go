package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
)

const hostSpec = `version: 1
name: shop
services:
  edge:
    type: haproxy
    upstreams:
      - service: api
        port: 8443
    host: true
apps:
  api:
    path: ./api
    replicas: 3
    host:
      port: 8443
`

// recordHostPort sets the host port in .bb/state.json, as the first bb up
// with a host target does.
func recordHostPort(t *testing.T, dir string, port int) {
	t.Helper()
	path := filepath.Join(dir, ".bb", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	st["hostPort"] = port
	if data, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// status returns host_urls and host_port; each target's row and its
// service_details carry its own URLs, and bb-host is marked as the router.
func TestHostAccessIsVisible(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "shop"})
	if err := os.WriteFile(filepath.Join(h.root, "experiment.yaml"), []byte(hostSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	// Before the first up no port is recorded: the fields are absent.
	st := h.mustCall(t, "status", nil)
	if _, ok := st["host_urls"]; ok {
		t.Errorf("status before up has host_urls: %v", st["host_urls"])
	}
	if _, ok := st["host_port"]; ok {
		t.Error("status before up has host_port")
	}

	recordHostPort(t, h.root, 44312)
	st = h.mustCall(t, "status", nil)
	if st["host_port"] != float64(44312) {
		t.Errorf("host_port = %v", st["host_port"])
	}
	var got []string
	for _, u := range st["host_urls"].([]any) {
		u := u.(map[string]any)
		inst, _ := u["instance"].(string)
		got = append(got, u["url"].(string)+" "+u["target"].(string)+" "+inst)
	}
	want := []string{
		"https://edge.shop.localhost:44312 edge ",
		"https://api.shop.localhost:44312 api ",
		"https://api-1.shop.localhost:44312 api api-1",
		"https://api-2.shop.localhost:44312 api api-2",
		"https://api-3.shop.localhost:44312 api api-3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("host_urls = %q", got)
	}
	rows := map[string]map[string]any{}
	for _, r := range st["services"].([]any) {
		r := r.(map[string]any)
		rows[r["name"].(string)] = r
	}
	if r := rows["bb-host"]; r == nil || r["host_router"] != true || r["host_urls"] != nil {
		t.Errorf("bb-host row = %v", r)
	}
	if r := rows["edge"]; r["host_router"] != nil || !slices.Equal(strs(r["host_urls"]), []string{"https://edge.shop.localhost:44312"}) {
		t.Errorf("edge row = %v", r)
	}
	if u := strs(rows["api-2"]["host_urls"]); !slices.Equal(u, []string{"https://api.shop.localhost:44312", "https://api-2.shop.localhost:44312"}) {
		t.Errorf("api-2 host_urls = %v", u)
	}

	d := h.mustCall(t, "service_details", map[string]any{"service": "api-3"})
	if u := strs(d["host_urls"]); !slices.Equal(u, []string{"https://api.shop.localhost:44312", "https://api-3.shop.localhost:44312"}) {
		t.Errorf("service_details api-3 host_urls = %v", u)
	}
	if d := h.mustCall(t, "service_details", map[string]any{"service": "bb-host"}); d["host_router"] != true || d["type"] != "traefik" {
		t.Errorf("service_details bb-host = %v %v", d["host_router"], d["type"])
	}
}

// up declares host_urls and host_port in its output schema, like status.
func TestUpAndStatusDeclareHostFields(t *testing.T) {
	tools := newHarness(t, false).tools(t)
	for _, name := range []string{"up", "status"} {
		b, err := json.Marshal(tools[name].OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"host_urls", "host_port"} {
			if _, ok := schema.Properties[key]; !ok {
				t.Errorf("%s output schema lacks %s", name, key)
			}
		}
	}
}

func TestHostViews(t *testing.T) {
	u := hostURLView(core.HostURL{URL: "https://api-2.shop.localhost:44312", Target: "api", Instance: "api-2"})
	if u.URL != "https://api-2.shop.localhost:44312" || u.Target != "api" || u.Instance != "api-2" {
		t.Errorf("hostURLView = %+v", u)
	}
	s := statusView(core.ServiceStatus{Name: "bb-host", HostRouter: true})
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["host_router"] != true {
		t.Errorf("host_router = %v", m["host_router"])
	}
	if _, ok := m["host_urls"]; ok {
		t.Error("an empty host_urls is not omitted")
	}
}
