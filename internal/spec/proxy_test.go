package spec

import (
	"slices"
	"strings"
	"testing"
)

// proxyLookup adds proxy types to the stand-in catalog: an HTTP proxy with
// every mode, and a pooler that takes exactly one postgres upstream.
func proxyLookup(id string) (TypeInfo, bool) {
	switch id {
	case "haproxy":
		return TypeInfo{HTTPS: true, Proxy: &ProxyInfo{Modes: []string{TLSOn, TLSOff, TLSPassthrough}, HTTP: true}}, true
	case "pooler":
		return TypeInfo{Proxy: &ProxyInfo{Modes: []string{TLSOn, TLSOff}, Targets: []string{"postgres"}, MinUpstreams: 1, MaxUpstreams: 1}}, true
	}
	return sidecarLookup(id)
}

func loadProxies(t *testing.T, src string) (*Spec, error) {
	t.Helper()
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	s, err := doc.Decode()
	if err != nil {
		return nil, err
	}
	return s, Validate(s, proxyLookup)
}

func TestProxyUpstreamsDecode(t *testing.T) {
	s, err := loadProxies(t, `version: 1
name: lab
services:
  consul:
    type: consul
  edge:
    type: haproxy
    upstreams:
      - web
      - service: api
        port: 9090
        name: v1
        tls: {mode: off, reason: "practice TLS termination"}
      - service: payments
        tls: {mode: passthrough}
      - service: consul
    file: ./edge/haproxy.cfg
apps:
  web:
    path: ./web
    replicas: 3
    register: {port: 8443}
    uses: [consul]
  api:
    path: ./api
  payments:
    path: ./payments
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  client:
    path: ./client
    uses: [edge]
`)
	if err != nil {
		t.Fatal(err)
	}
	edge, _ := s.Service("edge")
	if edge.File != "./edge/haproxy.cfg" || len(edge.Upstreams) != 4 {
		t.Fatalf("edge = %+v", edge)
	}
	u := edge.Upstreams
	if u[0].Service != "web" || u[0].Mode() != TLSOn || u[0].Route() != "web" {
		t.Errorf("short form = %+v", u[0])
	}
	if u[1].Port != 9090 || u[1].Route() != "v1" || u[1].Mode() != TLSOff || u[1].TLS.Reason == "" {
		t.Errorf("long form = %+v", u[1])
	}
	if u[2].Mode() != TLSPassthrough {
		t.Errorf("passthrough = %+v", u[2])
	}
}

func TestProxyUpstreamsValidation(t *testing.T) {
	_, err := loadProxies(t, `version: 1
name: lab
services:
  postgres:
    type: postgres
  kafka:
    type: kafka
    upstreams: [postgres]
    file: ./kafka.cfg
  redis:
    type: redis
  consul:
    type: consul
  edge:
    type: haproxy
    upstreams:
      - service: ghost
      - service: edge
      - service: api
        tls: {mode: off}
      - service: plain
      - service: redis
        tls: {mode: off, reason: x}
      - service: kafka
        port: 9093
      - service: web
        name: api
      - service: web
      - service: transparent
        tls: {mode: off, reason: x}
      - service: health
        tls: {mode: off, reason: x}
    file: /etc/haproxy.cfg
  edge2:
    type: haproxy
    upstreams:
      - service: api
        port: 9090
        tls: {mode: off, reason: x}
  pool:
    type: pooler
    upstreams:
      - service: web
      - service: redis
      - service: postgres
        tls: {mode: passthrough}
apps:
  api:
    path: ./api
  plain:
    path: ./plain
  web:
    path: ./web
    register: {port: 8443}
    uses: [consul]
  health:
    path: ./health
    health: {}
  transparent:
    path: ./t
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
        transparent: true
`)
	got := paths(t, err)
	want := []string{
		"services.kafka.upstreams",              // not a proxy
		"services.kafka.file",                   // not a proxy
		"services.edge.file",                    // absolute
		"services.edge.upstreams[0].service",    // no such target
		"services.edge.upstreams[1].service",    // itself
		"services.edge.upstreams[2].tls.reason", // off without reason
		"services.edge.upstreams[3].service",    // app without a TLS port
		"services.edge.upstreams[4].tls.mode",   // redis has no plaintext listener
		"services.edge.upstreams[4].service",    // redis does not speak HTTP
		"services.edge.upstreams[5].port",       // port on an infra target
		"services.edge.upstreams[5].service",    // kafka does not speak HTTP
		"services.edge.upstreams[6]",            // route api twice (upstreams[2] is api)
		"services.edge.upstreams[7].service",    // web twice
		"services.edge.upstreams[8].tls.mode",   // terminate into a transparent pod
		"services.edge.upstreams[9].port",       // plaintext on the health port
		"services.edge2.upstreams[0].port",      // another plaintext port for api
		"services.pool.upstreams",               // more than one
		"services.pool.upstreams[0].service",    // an app
		"services.pool.upstreams[1].service",    // not postgres
		"services.pool.upstreams[2].tls.mode",   // passthrough unsupported
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("paths:\n got %v\nwant %v\n%v", got, want, err)
	}
	for _, msg := range []string{"not a proxy", "must not be the proxy itself", "serves no TLS port", "does not speak HTTP", "transparent mode", "health port", "same port", "at most 1 upstream", "does not support passthrough"} {
		if !strings.Contains(err.Error(), msg) {
			t.Errorf("error lacks %q:\n%v", msg, err)
		}
	}
}

// A pooler takes exactly one postgres upstream: none, an empty list, two,
// and a target of another type or an app are each reported.
func TestPoolerUpstreamCardinality(t *testing.T) {
	head := `version: 1
name: lab
services:
  postgres:
    type: postgres
  postgres2:
    type: postgres
  redis:
    type: redis
`
	apps := `apps:
  web:
    path: ./web
`
	for name, tc := range map[string]struct {
		pool string
		want []string // violation paths
		msg  string
	}{
		"absent":       {"", []string{"services.pool.upstreams"}, "type pooler needs exactly 1 upstream (a postgres service); list it under upstreams"},
		"empty":        {"    upstreams: []\n", []string{"services.pool.upstreams"}, "needs exactly 1 upstream"},
		"one":          {"    upstreams: [postgres]\n", nil, ""},
		"two":          {"    upstreams: [postgres, postgres2]\n", []string{"services.pool.upstreams"}, "at most 1 upstream"},
		"another type": {"    upstreams: [redis]\n", []string{"services.pool.upstreams[0].service"}, "only takes postgres services as upstreams, not redis"},
		"an app":       {"    upstreams: [web]\n", []string{"services.pool.upstreams[0].service"}, "only takes postgres services as upstreams, not apps"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadProxies(t, head+"  pool:\n    type: pooler\n"+tc.pool+apps)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if got := paths(t, err); !slices.Equal(got, tc.want) {
				t.Errorf("paths = %v, want %v\n%v", got, tc.want, err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error lacks %q:\n%v", tc.msg, err)
			}
		})
	}
}

func TestProxyUpstreamDecodeErrors(t *testing.T) {
	doc, err := Parse([]byte(`version: 1
name: lab
services:
  edge:
    type: haproxy
    upstream: [web]
    upstreams:
      - service: web
        tls: {mode: maybe, clientAuth: require}
        colour: red
      - port: 0
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = doc.Decode()
	got := paths(t, err)
	want := []string{
		"services.edge.upstream",
		"services.edge.upstreams[0].tls.mode",
		"services.edge.upstreams[0].tls.clientAuth",
		"services.edge.upstreams[0].colour",
		"services.edge.upstreams[1].service",
		"services.edge.upstreams[1].port",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("paths:\n got %v\nwant %v\n%v", got, want, err)
	}
	if !strings.Contains(err.Error(), "must be on, off or passthrough") {
		t.Errorf("mode message: %v", err)
	}
}

// Without a TLS port of its own, an app still takes re-encrypt when the
// upstream names the port it serves TLS on.
func TestProxyUpstreamExplicitPort(t *testing.T) {
	_, err := loadProxies(t, `version: 1
name: lab
services:
  edge:
    type: haproxy
    upstreams:
      - service: web
        port: 8443
apps:
  web:
    path: ./web
`)
	if err != nil {
		t.Fatal(err)
	}
}

// static_file follows file's path rules and is accepted only by proxy
// types whose ProxyInfo.StaticFile is set.
func TestProxyStaticFileValidation(t *testing.T) {
	lookup := func(id string) (TypeInfo, bool) {
		if id == "traefik" {
			return TypeInfo{HTTPS: true, Proxy: &ProxyInfo{Modes: []string{TLSOn}, HTTP: true, StaticFile: true}}, true
		}
		return proxyLookup(id)
	}
	doc, err := Parse([]byte(`version: 1
name: lab
services:
  ok:
    type: traefik
    static_file: ./ok/static.yml
  abs:
    type: traefik
    static_file: /etc/traefik.yml
  inbb:
    type: traefik
    static_file: .bb/static.yml
  comma:
    type: traefik
    static_file: "./a,b.yml"
  edge:
    type: haproxy
    static_file: ./edge/static.yml
  redis:
    type: redis
    static_file: ./redis.yml
`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := doc.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Service("ok"); ok.StaticFile != "./ok/static.yml" {
		t.Errorf("static_file = %q", ok.StaticFile)
	}
	got := paths(t, Validate(s, lookup))
	want := []string{
		"services.abs.static_file",   // absolute
		"services.comma.static_file", // a comma
		"services.edge.static_file",  // haproxy has no static configuration to extend
		"services.inbb.static_file",  // into .bb
		"services.redis.static_file", // not a proxy
	}
	if !slices.Equal(got, want) {
		t.Errorf("violations:\n got %v\nwant %v", got, want)
	}
	msg := Validate(s, lookup).Error()
	for _, m := range []string{"services.edge.static_file: type haproxy has no static configuration a static_file can extend", "services.redis.static_file: type redis is not a proxy"} {
		if !strings.Contains(msg, m) {
			t.Errorf("error lacks %q:\n%s", m, msg)
		}
	}
}
