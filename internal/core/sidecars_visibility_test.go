package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/runtime"
)

const visibilitySpec = `version: 1
name: lab
services:
  postgres:
    type: postgres
apps:
  web:
    path: ./probe
    command: [probe, serve]
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  caller:
    path: ./probe
    command: [probe, serve]
    uses: [postgres]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
            outlier: {consecutive_5xx: 5, ejection: 30s}
        transparent: {egress: deny}
      - type: pgbouncer
        service: postgres
`

func visibilityHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.init()
	h.writeSpec(visibilitySpec)
	return h
}

func rowsByName(services []ServiceStatus) map[string]ServiceStatus {
	out := map[string]ServiceStatus{}
	for _, s := range services {
		out[s.Name] = s
	}
	return out
}

// Status lists each instance followed by its sidecars, with what each
// sidecar is, the ports it binds and the env it gives the app; the pod
// holder is never a row.
func TestStatusDescribesSidecarsAndReplicas(t *testing.T) {
	h := visibilityHarness(t)
	h.rt.containers["lab"] = []runtime.Container{
		{ID: "a", Service: "web-1", State: "running", Health: "healthy"},
		{ID: "b", Service: "web-1-pod", State: "exited", ExitCode: 137},
		{ID: "c", Service: "web-2", State: "running", Health: "healthy"},
		{ID: "d", Service: "web-2-pod", State: "running", Health: "healthy"},
		{ID: "e", Service: "web-2-envoy", State: "running", Health: "starting"},
	}
	res, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range res.Services {
		names = append(names, s.Name)
	}
	want := []string{"postgres", "web-1", "web-1-envoy", "web-2", "web-2-envoy", "web-3", "web-3-envoy", "caller", "caller-envoy", "caller-pgbouncer"}
	if !slices.Equal(names, want) {
		t.Fatalf("rows = %v\nwant %v", names, want)
	}
	rows := rowsByName(res.Services)
	if w := rows["web-1"]; w.State != "exited" || w.Health != "exited" {
		t.Errorf("web-1 with its pod holder down = %s/%s", w.State, w.Health)
	}
	w2 := rows["web-2"]
	if w2.App != "web" || w2.Instance != 2 || w2.Replicas != 3 || !slices.Equal(w2.Sidecars, []string{"web-2-envoy"}) || w2.Health != HealthHealthy {
		t.Errorf("web-2 = %+v", w2)
	}
	if c := rows["caller"]; c.App != "caller" || c.Instance != 0 || c.Replicas != 1 || len(c.Sidecars) != 2 {
		t.Errorf("caller = %+v", c)
	}
	e2 := rows["web-2-envoy"]
	if e2.Type != "envoy" || e2.Parent != "web-2" || e2.Health != HealthStarting || e2.Sidecar == nil {
		t.Fatalf("web-2-envoy = %+v", e2)
	}
	if !slices.ContainsFunc(e2.Sidecar.Ports, func(p SidecarPort) bool { return p.Port == 8443 && !p.Loopback }) ||
		!slices.ContainsFunc(e2.Sidecar.Ports, func(p SidecarPort) bool { return p.Port == 15000 && p.Loopback }) {
		t.Errorf("web-2-envoy ports = %+v", e2.Sidecar.Ports)
	}
	if len(e2.Sidecar.Env) != 1 || e2.Sidecar.Env[0] != (EnvVar{Name: "BB_LISTEN_ADDR", Value: "127.0.0.1:8080"}) {
		t.Errorf("web-2-envoy env = %+v", e2.Sidecar.Env)
	}

	ce := rows["caller-envoy"].Sidecar
	if ce == nil || !ce.Transparent || ce.Egress != "deny" || !slices.Equal(ce.Targets, []string{"web"}) {
		t.Fatalf("caller-envoy = %+v", ce)
	}
	for _, p := range []int{9001, 15001, 15006} {
		if !slices.ContainsFunc(ce.Ports, func(sp SidecarPort) bool { return sp.Port == p }) {
			t.Errorf("caller-envoy lacks port %d: %+v", p, ce.Ports)
		}
	}
	if !slices.ContainsFunc(ce.Settings, func(s SidecarSetting) bool {
		return s.Name == "outbound web" && strings.Contains(s.Value, "outlier ejection after 5 consecutive 5xx for 30s (at most 10% of hosts, swept every 10s)")
	}) {
		t.Errorf("caller-envoy settings = %+v", ce.Settings)
	}
	if len(ce.Env) != 1 || ce.Env[0].Name != "WEB_URL" || ce.Env[0].Value != "http://127.0.0.1:9001" {
		t.Errorf("caller-envoy env = %+v", ce.Env)
	}

	pb := rows["caller-pgbouncer"].Sidecar
	if pb == nil || pb.Type != "pgbouncer" || len(pb.Env) != 0 {
		t.Fatalf("caller-pgbouncer = %+v", pb)
	}
	rewrites := map[string]EnvVar{}
	for _, v := range pb.Rewrites {
		rewrites[v.Name] = v
	}
	if db := rewrites["DATABASE_URL"]; !db.Secret || db.Value != "" {
		t.Errorf("DATABASE_URL rewrite = %+v (secret values must be empty)", db)
	}
	if rewrites["PGHOST"].Value != "127.0.0.1" || rewrites["PGPORT"].Value != "6432" {
		t.Errorf("pgbouncer rewrites = %+v", pb.Rewrites)
	}

	var uses []Edge
	uses = rows["caller"].Uses
	if len(uses) != 2 || uses[0].To != "postgres" || uses[0].Via != "" || uses[1] != (Edge{To: "web", Port: 8443, TLS: true, Via: "envoy", ToApp: true}) {
		t.Errorf("caller uses = %+v", uses)
	}
	for _, s := range res.Services {
		if s.Sidecar != nil && s.Sidecar.Type == "" {
			t.Errorf("%s has no type", s.Name)
		}
	}
}

// ServiceDetails of an app lists its instances and sidecars; a sidecar's
// details say what it is. No secret value is returned.
func TestServiceDetailsDescribeSidecarsAndReplicas(t *testing.T) {
	h := visibilityHarness(t)
	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "web-1" || d.App != "web" || d.Instance != 1 || !slices.Equal(d.Replicas, []string{"web-1", "web-2", "web-3"}) || len(d.Sidecars) != 1 || d.Sidecars[0].Service != "web-1-envoy" {
		t.Errorf("web = %+v", d)
	}
	c, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "caller"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sidecars) != 2 || c.Sidecars[0].Name != "envoy" || c.Sidecars[1].Name != "pgbouncer" || !slices.Equal(c.Replicas, []string{"caller"}) {
		t.Errorf("caller sidecars = %+v replicas %v", c.Sidecars, c.Replicas)
	}
	var sidecarGroup bool
	for _, g := range c.Env {
		sidecarGroup = sidecarGroup || (g.Sidecar && g.Service == "caller-envoy")
		if len(g.Vars) == 0 {
			t.Errorf("empty env group %+v", g)
		}
		for _, v := range g.Vars {
			if v.Secret && v.Value != "" {
				t.Errorf("secret %s has a value", v.Name)
			}
		}
	}
	if !sidecarGroup {
		t.Errorf("no env group for caller-envoy: %+v", c.Env)
	}
	w, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "web-1"})
	if err != nil || !slices.Equal(w.UsedBy, []string{"caller"}) {
		t.Errorf("web-1 used by = %v %v", w.UsedBy, err)
	}
	s, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "web-2-envoy"})
	if err != nil || s.Kind != "sidecar" || s.Parent != "web-2" || s.Sidecar == nil || s.Sidecar.Type != "envoy" || s.Sidecar.Env[0].Name != "BB_LISTEN_ADDR" {
		t.Errorf("web-2-envoy = %+v %v", s, err)
	}
}

// The fixtures are real output of the explorers' scripts in the pinned
// envoyproxy/envoy and edoburu/pgbouncer images, captured from a lab with
// web (3 replicas, web-3 stopped) behind inbound Envoys and caller with an
// outbound Envoy and PgBouncer.
func TestSidecarExplorerParsers(t *testing.T) {
	out, err := parseEnvoy(fixture(t, "envoy-outbound.txt"), ".bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Listeners) != 1 || out.Listeners[0] != (EnvoyListener{Name: "outbound_web", Address: "127.0.0.1:9001"}) {
		t.Errorf("listeners = %+v", out.Listeners)
	}
	if len(out.Clusters) != 1 {
		t.Fatalf("clusters = %+v", out.Clusters)
	}
	c := out.Clusters[0]
	if c.Name != "outbound_web" || c.Retries != 15 || c.EjectionsOverflow != 6 || c.EjectionsActive != 0 || len(c.Hosts) != 3 {
		t.Fatalf("outbound_web = %+v", c)
	}
	var instances []string
	for _, h := range c.Hosts {
		instances = append(instances, h.Instance)
		if !h.Healthy || h.Hostname != "web.bb.internal" || !strings.HasSuffix(h.Address, ":8443") || h.Requests == 0 {
			t.Errorf("host = %+v", h)
		}
	}
	if !slices.Equal(instances, []string{"web-1", "web-2", "web-3"}) {
		t.Errorf("instances = %v", instances)
	}
	if w3 := c.Hosts[2]; w3.Errors != 15 || w3.Successes != 10 || w3.Requests != 25 {
		t.Errorf("web-3 (stopped app behind its Envoy) = %+v", w3)
	}

	in, err := parseEnvoy(fixture(t, "envoy-inbound.txt"), ".bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Clusters) != 1 || in.Clusters[0].Name != "app" || len(in.Clusters[0].Hosts) != 1 || in.Clusters[0].Hosts[0].Address != "127.0.0.1:8080" || in.Clusters[0].Hosts[0].Requests == 0 {
		t.Errorf("inbound clusters = %+v", in.Clusters)
	}
	if len(in.Listeners) != 1 || in.Listeners[0].Address != "0.0.0.0:8443" {
		t.Errorf("inbound listeners = %+v", in.Listeners)
	}

	if _, err := parseEnvoy("### clusters\nHTTP/1.1 503 Service Unavailable\r\ncontent-length: 0\r\n\r\n", ""); err == nil {
		t.Error("a 503 from the admin API parsed")
	}
	if _, err := parseEnvoy("bash: connect: Connection refused\n", ""); err == nil {
		t.Error("no admin response parsed")
	}

	pools, err := parsePgBouncer(fixture(t, "pgbouncer-show.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 {
		t.Fatalf("pools (admin console left out) = %+v", pools)
	}
	if p := pools[0]; p.Database != "app" || p.User != "app" || p.PoolMode != "transaction" || p.ServersIdle+p.ServersUsed != 1 || p.Transactions != 3 || p.Queries != 3 || p.BytesSent != 240 {
		t.Errorf("app pool = %+v", p)
	}
	if _, err := parsePgBouncer("### pools\nERROR: not allowed\n### stats\n"); err == nil {
		t.Error("psql error text parsed")
	}
}

// Envoy frames its responses with CRLF; a checkout that normalises line
// endings leaves bare LFs. Both parse, and chunk data is taken by size even
// when it holds line ends itself.
func TestHTTPBodyAcceptsCRLFAndLFFraming(t *testing.T) {
	for name, raw := range map[string]string{
		"crlf": "HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\ntransfer-encoding: chunked\r\n\r\n6\r\nab\ncd\n\r\n3\r\nef\r\r\n0\r\n\r\n",
		"lf":   "HTTP/1.1 200 OK\ncontent-type: text/plain\ntransfer-encoding: chunked\n\n6\nab\ncd\n\n3\nef\r\n0\n\n",
	} {
		body, err := httpBody(raw)
		if err != nil || string(body) != "ab\ncd\nef\r" {
			t.Errorf("%s: body %q %v", name, body, err)
		}
	}
	if body, err := httpBody("HTTP/1.1 200 OK\ncontent-length: 3\n\nabcdef"); err != nil || string(body) != "abc" {
		t.Errorf("content-length body %q %v", body, err)
	}
	if _, err := httpBody("HTTP/1.1 200 OK\ntransfer-encoding: chunked\n\n9\nshort\n"); err == nil {
		t.Error("a truncated chunk parsed")
	}
	for _, f := range []string{"envoy-outbound.txt", "envoy-inbound.txt"} {
		lf := fixture(t, f)
		crlf := crlfFraming(t, lf)
		if !strings.Contains(crlf, "\r\n") {
			t.Fatalf("%s: no CRLF framing produced", f)
		}
		a, errA := parseEnvoy(lf, ".bb.internal")
		b, errB := parseEnvoy(crlf, ".bb.internal")
		if errA != nil || errB != nil || len(a.Clusters) == 0 || len(a.Clusters) != len(b.Clusters) || len(a.Clusters[0].Hosts) != len(b.Clusters[0].Hosts) || a.Clusters[0].Hosts[0].Requests != b.Clusters[0].Hosts[0].Requests {
			t.Errorf("%s: LF %+v %v, CRLF %+v %v", f, a, errA, b, errB)
		}
	}
}

// crlfFraming rewrites the HTTP framing lines of an explorer capture (as
// stored with LF) to CRLF, as Envoy sent them, leaving chunk data as is.
func crlfFraming(t *testing.T, out string) string {
	t.Helper()
	var b strings.Builder
	rest := out
	next := func() string {
		l, r, _ := strings.Cut(rest, "\n")
		rest = r
		return l
	}
	for rest != "" {
		l := next()
		if !strings.HasPrefix(l, "HTTP/1.1 ") {
			b.WriteString(l + "\n")
			continue
		}
		b.WriteString(l + "\r\n")
		chunked := false
		for h := next(); ; h = next() {
			b.WriteString(h + "\r\n")
			if h == "" {
				break
			}
			chunked = chunked || strings.EqualFold(h, "transfer-encoding: chunked")
		}
		for chunked {
			sz := next()
			b.WriteString(sz + "\r\n")
			n, err := strconv.ParseInt(sz, 16, 64)
			if err != nil {
				t.Fatalf("chunk size %q", sz)
			}
			if n == 0 {
				b.WriteString(next() + "\r\n")
				break
			}
			b.WriteString(rest[:n])
			rest = rest[n:]
			b.WriteString(next() + "\r\n")
		}
	}
	return b.String()
}

func TestEnvoyHealthFlags(t *testing.T) {
	raw := func(m map[string]string) map[string]json.RawMessage {
		out := map[string]json.RawMessage{}
		for k, v := range m {
			out[k] = json.RawMessage(v)
		}
		return out
	}
	if f := healthFlags(raw(map[string]string{"eds_health_status": `"HEALTHY"`})); len(f) != 0 {
		t.Errorf("healthy host flags = %v", f)
	}
	f := healthFlags(raw(map[string]string{"eds_health_status": `"UNHEALTHY"`, "failed_outlier_check": "true", "pending_active_hc": "false"}))
	if !slices.Equal(f, []string{"eds_unhealthy", "failed_outlier_check"}) {
		t.Errorf("flags = %v", f)
	}
}

// The explorers find the sidecar from an app, an instance, or the
// sidecar's own name, and run their scripts inside it.
func TestSidecarExplorersRunInsideTheSidecar(t *testing.T) {
	h := visibilityHarness(t)
	h.rt.execOut = map[string]string{
		"caller-envoy":     fixture(t, "envoy-outbound.txt"),
		"web-2-envoy":      fixture(t, "envoy-inbound.txt"),
		"caller-pgbouncer": fixture(t, "pgbouncer-show.txt"),
	}
	req := func(name string) ExploreRequest { return ExploreRequest{Dir: h.dir, Service: name} }
	out, err := h.eng.EnvoyClusters(h.ctx(), req("caller"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Sidecar != "caller-envoy" || out.App != "caller" || out.Clusters[0].Role != "outbound" || out.Clusters[0].Target != "web" {
		t.Errorf("caller envoy = %+v", out)
	}
	in, err := h.eng.EnvoyClusters(h.ctx(), req("web-2"))
	if err != nil {
		t.Fatal(err)
	}
	if c := in.Clusters[0]; in.Sidecar != "web-2-envoy" || c.Role != "inbound" || c.Target != "web" || c.Hosts[0].Instance != "web-2" {
		t.Errorf("web-2 envoy = %+v", in)
	}
	if _, err := h.eng.EnvoyClusters(h.ctx(), req("web-2-envoy")); err != nil {
		t.Errorf("by sidecar name: %v", err)
	}
	pools, err := h.eng.PgBouncerPools(h.ctx(), req("caller-pgbouncer"))
	if err != nil {
		t.Fatal(err)
	}
	if pools.Sidecar != "caller-pgbouncer" || pools.Service != "postgres" || pools.PoolMode != "transaction" || pools.PoolSize != 10 || len(pools.Pools) != 1 {
		t.Errorf("pools = %+v", pools)
	}
	joined := strings.Join(h.rt.execs, "\n")
	for _, want := range []string{
		"caller-envoy: bash -c get() { exec 3<>/dev/tcp/127.0.0.1/15000",
		"bb-envoy web-1.bb.internal web-2.bb.internal web-3.bb.internal",
		"caller-pgbouncer: sh -c q() {",
		"bb-pgbouncer 6432",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no exec containing %q in:\n%s", want, joined)
		}
	}
	for _, tc := range []struct {
		name string
		call func(ExploreRequest) error
	}{
		{"postgres", func(r ExploreRequest) error { _, err := h.eng.EnvoyClusters(h.ctx(), r); return err }},
		{"web", func(r ExploreRequest) error { _, err := h.eng.PgBouncerPools(h.ctx(), r); return err }},
		{"nope", func(r ExploreRequest) error { _, err := h.eng.EnvoyClusters(h.ctx(), r); return err }},
	} {
		if err := tc.call(req(tc.name)); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: error = %v, want not found", tc.name, err)
		}
	}
}

// The explorers name Envoy's clusters as the generated configuration does.
func TestEnvoyClusterNamesMatchTheGeneratedConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "compose", "testdata", "transparent.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{envoyOutboundPrefix + "web", envoyAppCluster, envoyPassthrough, envoyInboundPassCluster} {
		if !strings.Contains(string(data), "- name: "+name+"\n") {
			t.Errorf("no cluster %q in the generated Envoy configuration", name)
		}
	}
}
