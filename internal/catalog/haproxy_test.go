package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// haproxyServer has an upstream in each mode, two of them apps with replicas.
func haproxyServer() Server {
	return Server{Name: "edge", FQDN: "edge.bb.internal", Domain: "bb.internal", Upstreams: []Upstream{
		{Name: "web", Service: "web", App: true, Mode: spec.TLSOn, Host: "web.bb.internal", ServerName: "web.bb.internal", Port: 8443,
			Servers: []string{"web-1.bb.internal", "web-2.bb.internal", "web-3.bb.internal"}},
		{Name: "api", Service: "api", App: true, Mode: spec.TLSOff, Reason: "practice termination\r\nobserve requests\nbackend be_evil\rserver x 1.2.3.4:80", Host: "api.plain.bb.internal", ServerName: "api.bb.internal", Port: 8080,
			Servers: []string{"api-1.plain.bb.internal", "api-2.plain.bb.internal"}},
		{Name: "payments", Service: "payments", App: true, Mode: spec.TLSPassthrough, Host: "payments.bb.internal", ServerName: "payments.bb.internal", Port: 8443,
			Servers: []string{"payments.bb.internal"}},
	}}
}

func TestHAProxyConfigShapes(t *testing.T) {
	e, _ := Lookup("haproxy")
	cfg := string(haproxyConfig(haproxyServer()))
	for _, want := range []string{
		"bind :8443 ssl crt " + KeystoreFile,
		"bind 127.0.0.1:8404", // stats and monitor on loopback only
		"nameserver dns 127.0.0.11:53",
		"use_backend be_api if { path_beg /api/ }",
		"default_backend be_web",
		"http-request replace-path /api/(.*) /\\1 if { path_beg /api/ }",
		"server-template web 3 web.bb.internal:8443 ssl verify required ca-file /etc/bb/ca.pem sni str(web.bb.internal) verifyhost web.bb.internal check",
		"server-template api 2 api.plain.bb.internal:8080 check",
		"bind :9443",
		"use_backend be_tls_payments if { req.ssl_sni -i payments.bb.internal }",
		"server-template payments 1 payments.bb.internal:8443 check",
		// A lab-sized connection limit on one thread, not auto-sizing
		// from the fd limit.
		"\n    maxconn 256\n",
		"\n    nbthread 1\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	// A multi-line reason stays comment: every line keeps the marker.
	for _, want := range []string{"    # observe requests\n", "    # backend be_evil\n", "    # server x 1.2.3.4:80\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "\nbackend be_evil") || strings.Contains(cfg, "\r") {
		t.Errorf("a reason line escaped its comment:\n%s", cfg)
	}
	// One upstream takes every request: no prefix routing or stripping.
	one := haproxyServer()
	one.Upstreams = one.Upstreams[:1]
	cfg = string(haproxyConfig(one))
	if strings.Contains(cfg, "path_beg") || strings.Contains(cfg, "replace-path") || strings.Contains(cfg, ":9443") {
		t.Errorf("a single upstream routes by path or listens for passthrough:\n%s", cfg)
	}
	// No upstream: the proxy still starts and answers 503.
	none := haproxyServer()
	none.Upstreams = nil
	if cfg := string(haproxyConfig(none)); strings.Contains(cfg, "default_backend") {
		t.Errorf("a proxy without upstreams has a backend:\n%s", cfg)
	}
	c := e.Server(haproxyServer())
	if len(c.Health.Test) == 0 || !strings.Contains(strings.Join(c.Health.Test, " "), "-verify_return_error") {
		t.Errorf("health check = %v", c.Health.Test)
	}
	if e.DataPath != "" {
		t.Error("a proxy has a data volume")
	}
}

func TestComment(t *testing.T) {
	got := Comment("    # ", "one\r\ntwo\rthree\n\nfour\x00five\u2028six")
	want := "    # one\n    # two\n    # three\n    #\n    # four five six"
	if got != want {
		t.Errorf("Comment = %q, want %q", got, want)
	}
	if got := Comment("# ", "single"); got != "# single" {
		t.Errorf("Comment = %q", got)
	}
}

func TestListenersFor(t *testing.T) {
	e, _ := Lookup("haproxy")
	if l := e.ListenersFor(nil); len(l) != 1 || l[0].Port != ProxyHTTPSPort {
		t.Errorf("without passthrough = %+v", l)
	}
	if l := e.ListenersFor(haproxyServer().Upstreams); len(l) != 2 || l[1].Port != ProxyPassthroughPort {
		t.Errorf("with passthrough = %+v", l)
	}
	pg, _ := Lookup("postgres")
	if l := pg.ListenersFor(haproxyServer().Upstreams); len(l) != 1 {
		t.Errorf("postgres = %+v", l)
	}
}

func TestProxyEnvAndUserFile(t *testing.T) {
	e, _ := Lookup("haproxy")
	env := e.Client(Client{Service: "api-gateway", FQDN: "api-gateway.bb.internal", Upstreams: haproxyServer().Upstreams})
	if len(env) != 2 || env[0].Name != "API_GATEWAY_URL" || env[0].Value != "https://api-gateway.bb.internal:8443" ||
		env[1].Name != "API_GATEWAY_PASSTHROUGH_ADDR" || env[1].Value != "api-gateway.bb.internal:9443" {
		t.Errorf("env = %+v", env)
	}
	if env := e.Client(Client{Service: "edge", FQDN: "edge.bb.internal"}); len(env) != 1 {
		t.Errorf("env without passthrough = %+v", env)
	}
	s := haproxyServer()
	s.UserConfig = []byte("# mine\n")
	c, err := e.Render(s)
	if err != nil || len(c.Files) != 1 || string(c.Files[0].Data) != "# mine\n" {
		t.Errorf("Render with a file = %+v, %v", c.Files, err)
	}
	pg, _ := Lookup("postgres")
	if _, err := pg.Render(Server{Name: "pg", Config: pg.ResolveConfig(nil), Secret: func(string) string { return "" }, UserConfig: []byte("x")}); err == nil {
		t.Error("a non-proxy accepted a file")
	}
	info, _ := Types("haproxy")
	if info.Proxy == nil || len(info.Proxy.Modes) != 3 || !info.Proxy.HTTP || !info.HTTPS {
		t.Errorf("type info = %+v", info)
	}
	if got := info.Env("edge", false); len(got) != 2 || got[0] != "EDGE_URL" {
		t.Errorf("Env = %v", got)
	}
}

// TestHAProxyConfigChecks runs haproxy -c inside the pinned image on the
// generated config, with a real certificate from a lab CA. It needs Docker.
func TestHAProxyConfigChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available")
	}
	t.Parallel()
	e, _ := Lookup("haproxy")
	now := time.Now()
	ca, err := pki.NewCA("bb.internal", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Issue(pki.LeafRequest{DNSNames: []string{"edge.bb.internal"}, Identity: pki.Identity("bb.internal", "lab", "edge")}, now)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string][]byte{
		"tls/keystore.pem":   append(append([]byte{}, leaf.KeyPEM...), leaf.CertPEM...), // as compose writes it
		"ca.pem":             ca.CertPEM,
		"config/haproxy.cfg": haproxyConfig(haproxyServer()),
		"config/single.cfg":  nil,
	}
	one := haproxyServer()
	one.Upstreams = nil
	files["config/single.cfg"] = haproxyConfig(one)
	for p, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{"haproxy.cfg", "single.cfg"} {
		out, err := exec.Command("docker", "run", "--rm", "--network", "none", "-v", dir+":/etc/bb:ro",
			e.Image, "haproxy", "-c", "-f", "/etc/bb/config/"+cfg).CombinedOutput()
		if err != nil {
			t.Errorf("haproxy -c %s: %v\n%s", cfg, err, out)
		} else {
			t.Logf("haproxy -c %s: %s", cfg, strings.TrimSpace(string(out)))
		}
	}
}
