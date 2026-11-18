package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/core"
)

// With a proxy the graph follows the traffic: clients, proxies, apps
// behind proxies, infrastructure. Upstream edges carry their mode; a
// terminate edge is dashed red with its reason; replicas stay one card.
func TestFlowLayoutDrawsProxyUpstreams(t *testing.T) {
	inst := func(app string, n, of int) row {
		r := svc(app+"-"+itoa(n), "app")
		r.App, r.Instance, r.Replicas = app, n, of

		return r
	}
	proxy := svc("edge", "infra",
		core.Edge{To: "web", Port: 8443, TLS: true, ToApp: true, Mode: "on"},
		core.Edge{To: "api", Port: 8080, ToApp: true, Mode: "off", Reason: "practice TLS termination"},
		core.Edge{To: "payments", Port: 8443, TLS: true, ToApp: true, Mode: "passthrough"},
		core.Edge{To: "postgres", Port: 5432, TLS: true, Mode: "passthrough"},
	)
	client := svc("client", "app", core.Edge{To: "edge", Port: 8443, TLS: true})
	rows := []row{
		proxy, svc("postgres", "infra"), svc("redis", "infra"),
		inst("web", 1, 3), inst("web", 2, 3), inst("web", 3, 3),
		inst("api", 1, 2), inst("api", 2, 2),
		svc("payments", "app", core.Edge{To: "redis", Port: 6380, TLS: true}),
		client,
	}
	g := layout(rows, "edge")

	var titles []string
	for _, c := range g.Columns {
		titles = append(titles, c.Title)
	}

	if strings.Join(titles, ",") != "YOUR CODE,PROXIES,BEHIND PROXIES,INFRASTRUCTURE" {
		t.Fatalf("columns = %v", titles)
	}

	at := map[string]node{}
	for _, n := range g.Nodes {
		at[n.Label()] = n
	}

	col := func(i int) int { return g.Columns[i].X }
	for name, c := range map[string]int{"client": 0, "edge": 1, "web": 2, "api": 2, "payments": 2, "postgres": 3, "redis": 3} {
		if at[name].X != col(c) {
			t.Errorf("%s at x %d, want column %d (x %d)", name, at[name].X, c, col(c))
		}
	}

	if at["web"].Replicas() != 3 || !at["edge"].Selected || len(g.Nodes) != 7 {
		t.Errorf("nodes = %+v", g.Nodes)
	}

	labels := map[string]edge{}
	for _, e := range g.Edges {
		labels[e.Label] = e
	}

	for _, want := range []string{"TLS :8443", "terminate :8080", "passthrough :8443", "passthrough :5432", "TLS :6380"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("no edge labelled %q in %+v", want, g.Edges)
		}
	}

	if term := labels["terminate :8080"]; !term.Plaintext || term.Reason != "practice TLS termination" {
		t.Errorf("terminate edge = %+v", term)
	}

	if pass := labels["passthrough :8443"]; pass.Plaintext {
		t.Errorf("passthrough edge = %+v", pass)
	}

	if len(g.Edges) != 6 {
		t.Errorf("edges = %d, want 6", len(g.Edges))
	}

	if g.Width != col(3)+nodeW+graphPad {
		t.Errorf("width %d, last column at %d", g.Width, col(3))
	}

	// Without proxies the two-column layout is unchanged.
	plain := layout([]row{svc("postgres", "infra"), svc("worker", "app", core.Edge{To: "postgres", Port: 5432, TLS: true})}, "")
	if len(plain.Columns) != 2 || plain.Columns[0].Title != "YOUR CODE" || plain.Columns[1].X != plain.InfraX {
		t.Errorf("two-column layout = %+v", plain.Columns)
	}
}

const proxySpec = `version: 1
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
    upstreams: [{service: web, port: 8443}, {service: api, tls: {mode: off, reason: practice TLS termination}}, {service: payments, port: 8443, tls: {mode: passthrough}}]
  router:
    type: traefik
    upstreams: [{service: web, port: 8443}, {service: api, tls: {mode: off, reason: practice TLS termination}}, {service: payments, port: 8443, tls: {mode: passthrough}}]
  front:
    type: nginx
    upstreams: [{service: web, port: 8443}, {service: api, tls: {mode: off, reason: practice TLS termination}}, {service: payments, port: 8443, tls: {mode: passthrough}}]
  postgres:
    type: postgres
  pool:
    type: pgbouncer
    upstreams: [postgres]
apps:
  web: {path: ./web, command: [web], replicas: 3}
  api: {path: ./web, command: [web], replicas: 2}
  payments: {path: ./web, command: [web]}
  client: {path: ./web, command: [web], uses: [edge, gw, router, front]}
  db: {path: ./web, command: [web], uses: [pool]}
`

// Proxy pages: the architecture graph with upstream edges by mode, each
// proxy's upstreams, its explorer tab, and no secret anywhere.
func TestProxyPages(t *testing.T) {
	fixture := func(name string) string {
		data, err := os.ReadFile(filepath.Join("..", "core", "testdata", "explore", name))
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}
	dir := t.TempDir()
	rt := fixtureRuntime{out: map[string]string{
		"edge":   fixture("haproxy-stats.txt"),
		"gw":     fixture("envoy-proxy.txt"),
		"router": fixture("traefik-api.txt"),
		"front":  fixture("nginx-stub-status.txt"),
		"pool":   fixture("pgbouncer-infra.txt"),
	}}

	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: rt, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := eng.Init(ctx, core.InitRequest{Dir: dir, Name: "lab"}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(proxySpec), 0o644); err != nil {
		t.Fatal(err)
	}

	creds, err := eng.Creds(ctx, core.CredsRequest{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(eng, Options{Dir: dir, Refresh: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.ln.Close() })

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for path, wants := range map[string][]string{
		"/architecture?node=edge": {
			"PROXIES", "BEHIND PROXIES", "TLS :8443", "terminate :8080", "passthrough :8443", `stroke-dasharray="6 4"`,
			"Reason: practice TLS termination", "Terminate: the proxy decrypts", "TLS forwarded by SNI",
		},
		"/services/edge": {
			"Upstreams", "web ×3", "api ×2", "re-encrypt", "terminate", "passthrough", "Reason: practice TLS termination",
			"/web/ (prefix stripped), and every other path, on :8443", "SNI payments.bb.internal on :9443, still encrypted",
			`href="/services/web-1"`, "EDGE_URL", "EDGE_PASSTHROUGH_ADDR", "?tab=explore", "HAProxy stats",
		},
		"/services/pool": {"Upstreams", "postgres", "every connection on :6432", "DATABASE_URL", "hidden", "PgBouncer pools"},
		"/fragments/explore/edge?tab=explore": {
			"be_web → web", "web-1", "172.21.0.2:8443", "No address (DNS)", "L6OK", "roundrobin",
			"be_api → api", "be_tls_payments → payments", "server-template", "bb logs edge -f",
		},
		"/fragments/explore/router?tab=explore": {
			"web@file → web", "default-web@file", "route-web@file", "PathPrefix(`/web/`)", "HostSNI(`payments.bb.internal`)",
			"passthrough", "Down", "every 2s /", "no per-server request counter", "bb logs router -f",
		},
		"/fragments/explore/front?tab=explore": {"stub_status", "205", "NGINX Plus", "web-3.bb.internal", "does not resolve", "10.213.0.10"},
		"/fragments/explore/gw?tab=explore":    {"web → web", "tls_payments → payments", "re-encrypt", "bb logs gw -f", "Listeners of gw"},
		"/fragments/explore/pool?tab=explore":  {"Pools of pool → postgres", "every app that uses pool", "app / app", "-p 6432"},
	} {
		rec := s.get(t, path, cookie)

		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", path, rec.Code)

			continue
		}

		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", path, w)
			}
		}

		if strings.Contains(body, "<script>") || strings.Contains(body, " style=") {
			t.Errorf("%s has an inline script or style", path)
		}

		for _, c := range creds.Services {
			for _, sec := range c.Secrets {
				if len(sec.Value) >= 8 && strings.Contains(body, sec.Value) {
					t.Errorf("%s shows the secret %s of %s", path, sec.Key, c.Name)
				}
			}
		}
	}
}
