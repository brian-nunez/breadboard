package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// A proxy's upstreams appear in status (with mode) and service_details
// (with route); the proxy explorers return each backend's servers, and
// envoy_clusters and pgbouncer_pools take infra services.
func TestProxiesAreVisible(t *testing.T) {
	h := newHarness(t, false)
	h.mustCall(t, "init", map[string]any{"name": "lab"})

	spec := `version: 1
name: lab
services:
  edge:
    type: haproxy
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
  gw:
    type: envoy
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
  router:
    type: traefik
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
  front:
    type: nginx
    upstreams: [{service: web, port: 8443}]
  postgres:
    type: postgres
  pool:
    type: pgbouncer
    upstreams: [postgres]
apps:
  web: {path: ./probe, replicas: 3}
  api: {path: ./probe, replicas: 2}
  payments: {path: ./probe}
  client: {path: ./probe, uses: [edge]}
`
	if err := os.WriteFile(filepath.Join(h.root, "experiment.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	st := h.mustCall(t, "status", nil)
	for _, s := range st["services"].([]any) {
		if s := s.(map[string]any); s["name"] == "edge" {
			uses := s["uses"].([]any)
			if e := uses[1].(map[string]any); e["to"] != "api" || e["mode"] != "off" || e["tls"] != false {
				t.Errorf("edge uses = %v", uses)
			}
		}
	}

	d := h.mustCall(t, "service_details", map[string]any{"service": "edge"})

	ups := d["upstreams"].([]any)
	if len(ups) != 3 {
		t.Fatalf("upstreams = %v", ups)
	}

	if u := ups[2].(map[string]any); u["mode"] != "passthrough" || u["sni"] != "payments.bb.internal" || u["listen_port"] != 9443.0 {
		t.Errorf("payments upstream = %v", u)
	}

	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "core", "testdata", "explore", name))
		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	h.rt.mu.Lock()
	h.rt.exec = map[string][]byte{
		"edge":   read("haproxy-stats.txt"),
		"router": read("traefik-api.txt"),
		"front":  read("nginx-stub-status.txt"),
		"gw":     read("envoy-proxy.txt"),
		"pool":   read("pgbouncer-infra.txt"),
	}
	h.rt.mu.Unlock()

	hs := h.mustCall(t, "haproxy_stats", map[string]any{"service": "edge"})
	be := hs["backends"].([]any)[0].(map[string]any)

	sv := be["servers"].([]any)[0].(map[string]any)
	if be["upstream"].(map[string]any)["mode"] != "on" || sv["instance"] != "web-1" || sv["total_sessions"] != 20.0 || sv["up"] != true {
		t.Errorf("haproxy_stats backend = %v", be)
	}

	tr := h.mustCall(t, "traefik_routes", map[string]any{"service": "router"})
	if len(tr["routers"].([]any)) != 4 || len(tr["services"].([]any)) != 3 {
		t.Errorf("traefik_routes = %v", tr)
	}

	ng := h.mustCall(t, "nginx_status", map[string]any{"service": "front"})
	if ng["requests"] != 205.0 || len(ng["upstreams"].([]any)) != 1 {
		t.Errorf("nginx_status = %v", ng)
	}

	env := h.mustCall(t, "envoy_clusters", map[string]any{"service": "gw"})
	if env["proxy"] != "gw" || env["sidecar"] != nil || len(env["clusters"].([]any)) != 3 {
		t.Errorf("envoy_clusters on an infra Envoy = %v", env)
	}

	pb := h.mustCall(t, "pgbouncer_pools", map[string]any{"service": "pool"})
	if pb["proxy"] != "pool" || pb["service"] != "postgres" || pb["port"] != 6432.0 {
		t.Errorf("pgbouncer_pools on an infra pool = %v", pb)
	}
}
