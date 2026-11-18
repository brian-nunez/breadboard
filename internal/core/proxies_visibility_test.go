package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// proxyVisibilitySpec is the lab the explorer fixtures were captured from:
// every proxy type with an upstream in each mode it supports, web with 3
// replicas (web-3 stopped at capture time), and a pool in front of
// Postgres.
const proxyVisibilitySpec = `version: 1
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
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
  postgres:
    type: postgres
  pool:
    type: pgbouncer
    config: {pool_size: 4}
    upstreams: [postgres]
apps:
  web:
    path: ./probe
    replicas: 3
  api:
    path: ./probe
    replicas: 2
  payments:
    path: ./probe
  client:
    path: ./probe
    uses: [edge, gw, router, front]
  db:
    path: ./probe
    uses: [pool]
`

// HAProxy's stats CSV, captured with web-3 stopped: per server its slot,
// the replica DNS gave it, its status and check, and its traffic.
func TestParseHAProxyStats(t *testing.T) {
	units := map[string]ProxyUpstream{
		"be_web":          {Route: "web", Target: "web", Mode: "on"},
		"be_api":          {Route: "api", Target: "api", Mode: "off"},
		"be_tls_payments": {Route: "payments", Target: "payments", Mode: "passthrough"},
	}

	res, err := parseHAProxy(fixture(t, "haproxy-stats.txt"), "bb.internal", units)
	if err != nil {
		t.Fatal(err)
	}

	var fronts []string
	for _, f := range res.Frontends {
		fronts = append(fronts, f.Name+"/"+f.Mode+"/"+f.Status)
	}

	if !slices.Equal(fronts, []string{"monitor/http/OPEN", "https/http/OPEN", "passthrough/tcp/OPEN"}) {
		t.Errorf("frontends = %v", fronts)
	}

	if res.Frontends[1].Requests != 110 || res.Frontends[1].TotalSessions != 34 {
		t.Errorf("https frontend = %+v", res.Frontends[1])
	}

	if len(res.Backends) != 3 {
		t.Fatalf("backends = %+v", res.Backends)
	}

	web := res.Backends[0]
	if web.Name != "be_web" || web.Upstream != units["be_web"] || web.Balance != "roundrobin" || web.Mode != "http" || web.TotalSessions != 60 || len(web.Servers) != 3 {
		t.Fatalf("be_web = %+v", web)
	}

	w1, w3 := web.Servers[0], web.Servers[2]
	if w1.Name != "web1" || w1.Address != "172.21.0.2:8443" || w1.Instance != "web-1" || !w1.Up || w1.CheckStatus != "L6OK" || w1.CheckDesc != "Layer6 check passed" || w1.TotalSessions != 20 || w1.Selected != 20 || w1.BytesIn == 0 {
		t.Errorf("web1 = %+v", w1)
	}
	// web-3 stopped: its name no longer resolves, so HAProxy parks the slot.
	if w3.Status != "MAINT (resolution)" || w3.Up || w3.Address != "" || w3.Instance != "" || w3.TotalSessions != 20 {
		t.Errorf("web3 = %+v", w3)
	}
	// Slot numbers are not replica numbers: api1 holds api-2's address.
	api := res.Backends[1]
	if api.Upstream.Mode != "off" || api.Servers[0].Name != "api1" || api.Servers[0].Instance != "api-2" || api.Servers[1].Instance != "api-1" {
		t.Errorf("be_api = %+v", api)
	}

	pay := res.Backends[2]
	if pay.Mode != "tcp" || pay.Upstream.Mode != "passthrough" || len(pay.Servers) != 1 || pay.Servers[0].Instance != "payments" || pay.Servers[0].CheckStatus != "L4OK" {
		t.Errorf("be_tls_payments = %+v", pay)
	}

	for _, bad := range []string{
		"bash: connect: Connection refused\n",
		"### stats\nHTTP/1.1 503 Service Unavailable\r\ncontent-length: 0\r\n\r\n",
		"### stats\nHTTP/1.1 200 OK\r\ncontent-length: 6\r\n\r\nhello\n",
	} {
		if _, err := parseHAProxy(bad, "bb.internal", nil); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}
}

// Traefik's API, captured with web-3 stopped: routers with their rules
// and TLS handling, services with each server's health status.
func TestParseTraefikRoutes(t *testing.T) {
	units := map[string]ProxyUpstream{
		"web":          {Route: "web", Target: "web", Mode: "on"},
		"api":          {Route: "api", Target: "api", Mode: "off"},
		"tls-payments": {Route: "payments", Target: "payments", Mode: "passthrough"},
	}

	res, err := parseTraefik(fixture(t, "traefik-api.txt"), "bb.internal", units)
	if err != nil {
		t.Fatal(err)
	}

	var routers []string
	for _, r := range res.Routers {
		routers = append(routers, r.Protocol+":"+r.Name+" "+r.Rule+" -> "+r.Service+" ("+r.TLS+")")
	}

	want := []string{
		"http:default-web@file PathPrefix(`/`) -> web (terminate)",
		"http:route-api@file PathPrefix(`/api/`) -> api (terminate)",
		"http:route-web@file PathPrefix(`/web/`) -> web (terminate)",
		"tcp:tls-payments@file HostSNI(`payments.bb.internal`) -> tls-payments (passthrough)",
	}
	if !slices.Equal(routers, want) {
		t.Errorf("routers (Traefik's own @internal ones left out):\n%s", strings.Join(routers, "\n"))
	}

	if r := res.Routers[1]; !slices.Equal(r.Middlewares, []string{"strip-api@file"}) || r.Status != "enabled" || !slices.Equal(r.EntryPoints, []string{"websecure"}) {
		t.Errorf("api router = %+v", r)
	}

	if len(res.Services) != 3 {
		t.Fatalf("services = %+v", res.Services)
	}

	var web *TraefikService

	for i := range res.Services {
		if res.Services[i].Name == "web@file" {
			web = &res.Services[i]
		}
	}

	if web == nil || web.Upstream != units["web"] || web.Strategy != "wrr" || web.HealthCheck != "every 2s /" || len(web.Servers) != 3 {
		t.Fatalf("web service = %+v", web)
	}

	var states []string
	for _, s := range web.Servers {
		states = append(states, s.Instance+"="+s.Status)
	}

	if !slices.Equal(states, []string{"web-1=UP", "web-2=UP", "web-3=DOWN"}) {
		t.Errorf("web servers = %v", states)
	}

	for _, s := range res.Services {
		switch s.Name {
		case "api@file":
			if s.Upstream.Mode != "off" || s.Servers[0].Instance != "api-1" || s.Servers[0].Address != "http://api-1.plain.bb.internal:8080" {
				t.Errorf("api service = %+v", s)
			}
		case "tls-payments@file":
			if s.Protocol != "tcp" || s.Upstream.Mode != "passthrough" || s.HealthCheck != "every 2s (TCP connect)" || s.Servers[0].Instance != "payments" || s.Servers[0].Status != "UP" {
				t.Errorf("payments service = %+v", s)
			}
		}
	}

	if _, err := parseTraefik("### http/routers\n\n### http/services\n[]\n", "bb.internal", nil); err == nil || !strings.Contains(err.Error(), "/api/http/routers") {
		t.Errorf("unreachable API: %v", err)
	}
}

// NGINX's stub_status, and the names its upstreams resolve to.
func TestParseNginxStatus(t *testing.T) {
	res, err := parseNginx(fixture(t, "nginx-stub-status.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if res.Active != 1 || res.Accepts != 94 || res.Handled != 94 || res.Requests != 205 || res.Reading != 0 || res.Writing != 1 || res.Waiting != 0 {
		t.Errorf("stub_status = %+v", res)
	}

	if _, err := parseNginx("### stub_status\nwget: can't connect to remote host: Connection refused\n### hosts\n"); err == nil {
		t.Error("no error without stub_status")
	}
}

// The infra Envoy and PgBouncer fixtures parse with the sidecar parsers.
func TestInfraEnvoyAndPgBouncerFixturesParse(t *testing.T) {
	res, err := parseEnvoy(fixture(t, "envoy-proxy.txt"), ".bb.internal")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, c := range res.Clusters {
		names = append(names, c.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"api", "tls_payments", "web"}) {
		t.Errorf("clusters = %v", names)
	}

	pools, err := parsePgBouncer(fixture(t, "pgbouncer-infra.txt"))
	if err != nil || len(pools) != 1 || pools[0].Database != "app" || pools[0].Queries != 9 {
		t.Errorf("pools = %+v, %v", pools, err)
	}
}

func proxyHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.init()
	h.writeSpec(proxyVisibilitySpec)
	h.rt.execOut = map[string]string{
		"edge":   fixture(t, "haproxy-stats.txt"),
		"gw":     fixture(t, "envoy-proxy.txt"),
		"router": fixture(t, "traefik-api.txt"),
		"front":  fixture(t, "nginx-stub-status.txt"),
		"pool":   fixture(t, "pgbouncer-infra.txt"),
	}

	return h
}

// The proxy explorers run inside the proxy's container, resolve the
// instances of every upstream there, and name each backend after the
// upstream it balances.
func TestProxyExplorersRunInsideTheProxy(t *testing.T) {
	h := proxyHarness(t)
	req := func(name string) ExploreRequest { return ExploreRequest{Dir: h.dir, Service: name} }

	hs, err := h.eng.HAProxyStats(h.ctx(), req("edge"))
	if err != nil {
		t.Fatal(err)
	}

	if hs.Service != "edge" || hs.Backends[0].Upstream != (ProxyUpstream{Route: "web", Target: "web", Mode: "on"}) || hs.Backends[2].Upstream.Mode != "passthrough" {
		t.Errorf("haproxy = %+v", hs)
	}

	tr, err := h.eng.TraefikRoutes(h.ctx(), req("router"))
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range tr.Services {
		if s.Upstream.Target == "" {
			t.Errorf("Traefik service %s matches no upstream", s.Name)
		}
	}

	ng, err := h.eng.NginxStatus(h.ctx(), req("front"))
	if err != nil {
		t.Fatal(err)
	}

	if ng.Service != "front" || ng.Requests != 205 || len(ng.Upstreams) != 3 {
		t.Fatalf("nginx = %+v", ng)
	}

	var backends []string
	for _, b := range ng.Upstreams[0].Backends {
		backends = append(backends, b.Instance+"="+b.Address)
	}

	if ng.Upstreams[0].Route != "web" || ng.Upstreams[0].Mode != "on" || ng.Upstreams[0].Port != 8443 || !slices.Equal(backends, []string{"web-1=172.21.0.2", "web-2=172.21.0.7", "web-3="}) {
		t.Errorf("nginx web upstream = %+v (%v)", ng.Upstreams[0], backends)
	}

	if b := ng.Upstreams[1].Backends; ng.Upstreams[1].Mode != "off" || b[0].Name != "api-1.plain.bb.internal" || b[0].Instance != "api-1" || b[0].Address != "10.213.0.10" {
		t.Errorf("nginx api upstream = %+v", ng.Upstreams[1])
	}

	env, err := h.eng.EnvoyClusters(h.ctx(), req("gw"))
	if err != nil {
		t.Fatal(err)
	}

	if env.Proxy != "gw" || env.Sidecar != "" || len(env.Listeners) != 2 {
		t.Errorf("envoy = %+v", env)
	}

	for _, c := range env.Clusters {
		if c.Role != "upstream" || c.Target == "" || c.Mode == "" {
			t.Errorf("cluster %s = role %q target %q mode %q", c.Name, c.Role, c.Target, c.Mode)
		}

		for _, host := range c.Hosts {
			if host.Instance == "" || strings.Contains(host.Instance, ".") {
				t.Errorf("cluster %s host %s instance %q", c.Name, host.Address, host.Instance)
			}
		}

		if c.Name == "api" && (c.Mode != "off" || len(c.Hosts) != 2) {
			t.Errorf("api cluster = %+v", c)
		}
	}

	pools, err := h.eng.PgBouncerPools(h.ctx(), req("pool"))
	if err != nil {
		t.Fatal(err)
	}

	if pools.Proxy != "pool" || pools.Sidecar != "" || pools.Service != "postgres" || pools.PoolMode != "transaction" || pools.PoolSize != 4 || pools.Port != 6432 || len(pools.Pools) != 1 {
		t.Errorf("pools = %+v", pools)
	}

	joined := strings.Join(h.rt.execs, "\n")

	instances := "web-1.bb.internal web-2.bb.internal web-3.bb.internal api-1.plain.bb.internal api-2.plain.bb.internal payments.bb.internal"
	for _, want := range []string{
		"edge: bash -c exec 3<>/dev/tcp/127.0.0.1/8404 && printf 'GET /stats;csv HTTP/1.1",
		"bb-haproxy " + instances,
		"router: sh -c for p in http/routers http/services tcp/routers tcp/services; do",
		"http://127.0.0.1:8080/api/$p",
		"front: sh -c echo '### stub_status'; wget -qO- http://127.0.0.1:8404/stub_status",
		"bb-nginx " + instances,
		"gw: bash -c get() { exec 3<>/dev/tcp/127.0.0.1/15000",
		"bb-envoy " + instances,
		"pool: sh -c q() {",
		"bb-pgbouncer 6432",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no exec containing %q in:\n%s", want, joined)
		}
	}

	// Each explorer takes only its own type.
	for _, tc := range []struct {
		name string
		call func(ExploreRequest) error
	}{
		{"front", func(r ExploreRequest) error {
			_, err := h.eng.HAProxyStats(h.ctx(), r)

			return err
		}},
		{"edge", func(r ExploreRequest) error {
			_, err := h.eng.TraefikRoutes(h.ctx(), r)

			return err
		}},
		{"router", func(r ExploreRequest) error {
			_, err := h.eng.NginxStatus(h.ctx(), r)

			return err
		}},
		{"edge", func(r ExploreRequest) error {
			_, err := h.eng.EnvoyClusters(h.ctx(), r)

			return err
		}},
		{"postgres", func(r ExploreRequest) error {
			_, err := h.eng.PgBouncerPools(h.ctx(), r)

			return err
		}},
		{"web", func(r ExploreRequest) error {
			_, err := h.eng.HAProxyStats(h.ctx(), r)

			return err
		}},
	} {
		if err := tc.call(req(tc.name)); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: error = %v, want not found", tc.name, err)
		}
	}
}

// A proxy's details list its upstreams with mode, port and route.
func TestServiceDetailsListProxyUpstreams(t *testing.T) {
	h := proxyHarness(t)

	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "edge"})
	if err != nil {
		t.Fatal(err)
	}

	if len(d.Upstreams) != 3 {
		t.Fatalf("upstreams = %+v", d.Upstreams)
	}

	web, api, pay := d.Upstreams[0], d.Upstreams[1], d.Upstreams[2]
	if web.Service != "web" || !web.App || web.Instances != 3 || web.Mode != "on" || web.Port != 8443 || web.ListenPort != 8443 || web.PathPrefix != "/web/" || !web.Default ||
		web.Route != "/web/ (prefix stripped), and every other path, on :8443" {
		t.Errorf("web = %+v", web)
	}

	if api.Mode != "off" || api.Reason != "practice TLS termination" || api.Port != 8080 || api.Instances != 2 || api.Default || api.Route != "/api/ (prefix stripped) on :8443" {
		t.Errorf("api = %+v", api)
	}

	if pay.Mode != "passthrough" || pay.ListenPort != 9443 || pay.SNI != "payments.bb.internal" || pay.PathPrefix != "" || pay.Route != "SNI payments.bb.internal on :9443, still encrypted" {
		t.Errorf("payments = %+v", pay)
	}

	pool, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "pool"})
	if err != nil {
		t.Fatal(err)
	}

	if len(pool.Upstreams) != 1 || pool.Upstreams[0].Service != "postgres" || pool.Upstreams[0].App || pool.Upstreams[0].ListenPort != 6432 || pool.Upstreams[0].Route != "every connection on :6432" {
		t.Errorf("pool upstreams = %+v", pool.Upstreams)
	}

	for _, g := range pool.Env {
		for _, v := range g.Vars {
			if v.Secret && v.Value != "" {
				t.Errorf("%s leaks a secret value", v.Name)
			}
		}
	}

	if app, _ := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "client"}); len(app.Upstreams) != 0 {
		t.Errorf("an app has upstreams: %+v", app.Upstreams)
	}
}
