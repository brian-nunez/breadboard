package compose

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// proxyLab has an HAProxy with every upstream mode: re-encrypt to an app
// with 3 replicas, terminate to an app with 2 replicas and to an infra
// service, passthrough to an app behind an Envoy sidecar.
func proxyLab() *spec.Spec {
	return &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{
			{Name: "consul", Type: "consul"},
			{Name: "edge", Type: "haproxy", Upstreams: []spec.Upstream{
				{Service: "web", Port: 8443},
				{Service: "api", Port: 9090, Name: "v1", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "practice TLS termination\nand watch the requests"}},
				{Service: "consul", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "plain HTTP API behind the proxy"}},
				{Service: "payments", TLS: spec.TLS{Mode: spec.TLSPassthrough}},
			}},
		},
		Apps: []spec.App{
			{Name: "web", Path: "./web", Replicas: 3},
			{Name: "api", Path: "./api", Replicas: 2},
			{Name: "payments", Path: "./payments", Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
				Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080},
			}}}},
			{Name: "client", Path: "./client", Uses: []spec.Use{{Service: "edge"}}},
		},
	}
}

func TestHAProxyAllModesGolden(t *testing.T) {
	b := renderWith(t, proxyLab(), secrets.Inline, nil)
	checkCompose(t, b)
	golden(t, "haproxy", b)
}

// The proxy reaches a terminated app's replicas on their segment, every
// replica gets a static address and BB_PLAIN_LISTEN_ADDR, and no one else
// joins the segment.
func TestTerminateHopsGetASegment(t *testing.T) {
	b := renderWith(t, proxyLab(), secrets.Inline, nil)
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]struct {
			Environment map[string]string         `yaml:"environment"`
			Networks    map[string]map[string]any `yaml:"networks"`
			Volumes     []string                  `yaml:"volumes"`
			DependsOn   map[string]any            `yaml:"depends_on"`
			NetworkMode string                    `yaml:"network_mode"`
			Command     []string                  `yaml:"command"`
			CapAdd      []string                  `yaml:"cap_add"`
			Labels      map[string]string         `yaml:"labels"`
		} `yaml:"services"`
		Networks map[string]map[string]any `yaml:"networks"`
		Volumes  map[string]any            `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	for i, inst := range []string{"api-1", "api-2"} {
		// A terminate target runs as a pod: the holder owns the networks.
		s := doc.Services[inst+"-pod"]
		seg := s.Networks["plain-api"]
		wantAddr := []string{"10.213.1.10", "10.213.1.11"}[i]
		if seg["ipv4_address"] != wantAddr {
			t.Errorf("%s on plain-api = %v, want address %s", inst, seg, wantAddr)
		}
		aliases, _ := seg["aliases"].([]any)
		if !slices.Contains(aliases, any(inst+".plain.bb.internal")) || !slices.Contains(aliases, any("api.plain.bb.internal")) {
			t.Errorf("%s segment aliases = %v", inst, aliases)
		}
		app := doc.Services[inst]
		if got := app.Environment["BB_PLAIN_LISTEN_ADDR"]; got != wantAddr+":9090" {
			t.Errorf("%s BB_PLAIN_LISTEN_ADDR = %q", inst, got)
		}
		if app.NetworkMode != "service:"+inst+"-pod" || len(app.CapAdd) > 0 || app.Labels[LabelCapture] != "guard" {
			t.Errorf("%s = mode %q caps %v capture %q", inst, app.NetworkMode, app.CapAdd, app.Labels[LabelCapture])
		}
		// Its netinit, the only holder of NET_ADMIN, drops the plaintext
		// port on every address but the segment's, before the app starts.
		ni := doc.Services[inst+"-netinit"]
		script := strings.Join(ni.Command, " ")
		if !slices.Equal(ni.CapAdd, []string{"NET_ADMIN"}) || !strings.Contains(script, "--dport 9090 ! -d "+wantAddr+" -j DROP") {
			t.Errorf("%s-netinit = caps %v command %q", inst, ni.CapAdd, script)
		}
		if _, ok := app.DependsOn[inst+"-netinit"]; !ok {
			t.Errorf("%s does not wait for its netinit: %v", inst, app.DependsOn)
		}
	}
	for name, s := range doc.Services {
		_, onSeg := s.Networks["plain-api"]
		want := name == "edge" || name == "api-1-pod" || name == "api-2-pod"
		if onSeg != want {
			t.Errorf("%s on plain-api = %v, want %v", name, onSeg, want)
		}
		if _, ok := s.Environment["BB_PLAIN_LISTEN_ADDR"]; ok && !strings.HasPrefix(name, "api-") {
			t.Errorf("%s receives BB_PLAIN_LISTEN_ADDR", name)
		}
	}
	if _, ok := doc.Services["edge"].Networks["plain-consul"]; !ok {
		t.Errorf("edge is not on consul's segment: %v", doc.Services["edge"].Networks)
	}
	if internal, _ := doc.Networks["plain-api"]["internal"].(bool); !internal {
		t.Errorf("plain-api is not internal: %v", doc.Networks["plain-api"])
	}
	// Docker hands out only the upper half of an app's segment, so proxies
	// never take an address a replica needs; infra segments are unchanged.
	ipam := func(net string) map[string]any {
		cfg := doc.Networks[net]["ipam"].(map[string]any)["config"].([]any)
		return cfg[0].(map[string]any)
	}
	if got := ipam("plain-api")["ip_range"]; got != "10.213.1.128/25" {
		t.Errorf("plain-api ip_range = %v", got)
	}
	if _, ok := ipam("plain-consul")["ip_range"]; ok {
		t.Errorf("plain-consul has an ip_range: %v", ipam("plain-consul"))
	}
	// Apps without a terminating proxy are untouched.
	if _, ok := doc.Services["web-1-pod"]; ok {
		t.Error("a re-encrypt target became a pod")
	}
	// Proxies keep no data and never wait for their upstreams.
	if _, ok := doc.Volumes["edge-data"]; ok {
		t.Error("the proxy has a data volume")
	}
	if len(doc.Services["edge"].DependsOn) > 0 {
		t.Errorf("edge depends on %v", doc.Services["edge"].DependsOn)
	}
	env := doc.Services["client"].Environment
	if env["EDGE_URL"] != "https://edge.bb.internal:8443" || env["EDGE_PASSTHROUGH_ADDR"] != "edge.bb.internal:9443" {
		t.Errorf("client env = %v", env)
	}
}

func TestProxyEdgesAndWarnings(t *testing.T) {
	m, err := model.Resolve(proxyLab(), "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range m.EdgesFrom("edge") {
		got = append(got, e.To+"/"+e.Mode+"/"+e.Listener)
	}
	want := []string{"web/on/tls", "api/off/plain", "consul/off/https", "payments/passthrough/tls"}
	if !slices.Equal(got, want) {
		t.Errorf("edges = %v, want %v", got, want)
	}
	var plain []string
	for _, e := range m.PlaintextEdges() {
		plain = append(plain, e.From+"->"+e.To)
	}
	if !slices.Equal(plain, []string{"edge->api", "edge->consul"}) {
		t.Errorf("plaintext edges = %v", plain)
	}
	edge, _ := m.Service("edge")
	web := edge.Upstreams[0]
	if web.Host != "web.bb.internal" || web.ServerName != "web.bb.internal" || len(web.Servers) != 3 || web.Servers[2] != "web-3.bb.internal" {
		t.Errorf("web upstream = %+v", web)
	}
	api := edge.Upstreams[1]
	if api.Name != "v1" || api.Host != "api.plain.bb.internal" || api.Port != 9090 || api.Servers[1] != "api-2.plain.bb.internal" {
		t.Errorf("api upstream = %+v", api)
	}
	if p := edge.Upstreams[3]; p.Port != 8443 || p.Host != "payments.bb.internal" {
		t.Errorf("payments upstream = %+v", p)
	}
}

// file replaces the generated config; the copy changes the config hash.
func TestProxyUserFileReplacesTheConfig(t *testing.T) {
	s := proxyLab()
	s.Services[1].File = "edge/haproxy.cfg"
	b := renderWith(t, s, secrets.Inline, map[string]string{"edge/haproxy.cfg": "# mine\n"})
	f, ok := b.File("config/edge/haproxy.cfg")
	if !ok || string(f.Data) != "# mine\n" {
		t.Fatalf("config = %q", f.Data)
	}
	gen := renderWith(t, proxyLab(), secrets.Inline, nil)
	if hash(t, b, "edge") == hash(t, gen, "edge") {
		t.Error("the user's file does not change the config hash")
	}
	m, _ := model.Resolve(s, "bb.internal")
	_, err := renderModelErr(m, secrets.Inline, func(string) ([]byte, error) { return nil, errors.New("no such file") })
	if err == nil || !strings.Contains(err.Error(), "edge: file") {
		t.Errorf("missing file: %v", err)
	}
}

func hash(t *testing.T, b *Bundle, service string) string {
	t.Helper()
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Services[service].Labels[LabelConfigHash]
}
