package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/pki"
)

func TestNginxConfigShapes(t *testing.T) {
	e, _ := Lookup("nginx")

	cfg := string(nginxConfig(haproxyServer()))
	for _, want := range []string{
		"listen 8443 ssl;",
		"ssl_certificate " + CertFile + ";",
		"listen 127.0.0.1:8404;", // status and health on loopback only
		"stub_status;",
		"resolver 127.0.0.11 valid=5s ipv6=off;",
		"zone be_web 64k;",
		"server web.bb.internal:8443 resolve max_fails=3 fail_timeout=10s;",
		"server api.plain.bb.internal:8080 resolve max_fails=3 fail_timeout=10s;",
		"location / {\n            proxy_pass https://be_web;",
		"location /web/ {\n            proxy_pass https://be_web/;",
		"location /api/ {\n            proxy_pass http://be_api/;",
		"proxy_ssl_verify on;",
		"proxy_ssl_trusted_certificate " + CAFile + ";",
		"proxy_ssl_server_name on;",
		"proxy_ssl_name web.bb.internal;",
		"listen 9443;",
		"ssl_preread on;",
		"payments.bb.internal tls_payments;",
		"server payments.bb.internal:8443 resolve max_fails=3 fail_timeout=10s;",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}

	if strings.Contains(cfg, "    health_check") {
		t.Error("the config uses health_check, which is NGINX Plus only")
	}
	// The terminate location never verifies TLS: it speaks plain HTTP.
	if _, after, _ := strings.Cut(cfg, "location /api/ {"); strings.Contains(strings.SplitN(after, "}", 2)[0], "proxy_ssl") {
		t.Errorf("the terminate location sets proxy_ssl directives:\n%s", cfg)
	}
	// One upstream takes every request: no prefix routing or stream block.
	one := haproxyServer()
	one.Upstreams = one.Upstreams[:1]

	cfg = string(nginxConfig(one))
	if strings.Contains(cfg, "location /web/") || strings.Contains(cfg, "stream {") || strings.Count(cfg, "location / {") != 1 {
		t.Errorf("a single upstream routes by path or listens for passthrough:\n%s", cfg)
	}
	// No upstream: the proxy still starts and answers 503.
	none := haproxyServer()

	none.Upstreams = nil
	if cfg := string(nginxConfig(none)); strings.Contains(cfg, "upstream be_") || !strings.Contains(cfg, "return 503;") {
		t.Errorf("a proxy without upstreams:\n%s", cfg)
	}

	c := e.Server(haproxyServer())
	if h := strings.Join(c.Health.Test, " "); !strings.Contains(h, "--cacert "+CAFile) || !strings.Contains(h, "127.0.0.1:8404/healthz") || !strings.Contains(h, "-X TRACE") {
		t.Errorf("health check = %v", c.Health.Test)
	}

	if e.DataPath != "" {
		t.Error("a proxy has a data volume")
	}

	info, _ := Types("nginx")
	if info.Proxy == nil || len(info.Proxy.Modes) != 3 || !info.Proxy.HTTP || !info.HTTPS {
		t.Errorf("type info = %+v", info)
	}

	if got := info.Env("edge", false); len(got) != 2 || got[0] != "EDGE_URL" || got[1] != "EDGE_PASSTHROUGH_ADDR" {
		t.Errorf("Env = %v", got)
	}
}

// TestNginxConfigChecks runs nginx -t inside the pinned image on the
// generated configs, with a real certificate from a lab CA. It needs
// Docker.
func TestNginxConfigChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}

	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available")
	}

	t.Parallel()

	e, _ := Lookup("nginx")
	now := time.Now()

	ca, err := pki.NewCA("bb.internal", now)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := ca.Issue(pki.LeafRequest{DNSNames: []string{"edge.bb.internal"}, Identity: pki.Identity("bb.internal", "lab", "edge")}, now)
	if err != nil {
		t.Fatal(err)
	}

	one, none := haproxyServer(), haproxyServer()
	one.Upstreams, none.Upstreams = one.Upstreams[:1], nil
	dir := t.TempDir()
	files := map[string][]byte{
		"tls/tls.crt":       leaf.CertPEM,
		"tls/tls.key":       leaf.KeyPEM,
		"ca.pem":            ca.CertPEM,
		"config/all.conf":   nginxConfig(haproxyServer()),
		"config/one.conf":   nginxConfig(one),
		"config/empty.conf": nginxConfig(none),
	}
	writeTree(t, dir, files)

	for _, cfg := range []string{"all.conf", "one.conf", "empty.conf"} {
		// --network none: the upstream names cannot resolve, as when the
		// backends start after the proxy.
		out, err := exec.Command("docker", "run", "--rm", "--network", "none", "-v", dir+":/etc/bb:ro",
			"--entrypoint", "nginx", e.Image, "-t", "-c", "/etc/bb/config/"+cfg).CombinedOutput()
		if err != nil || strings.Contains(string(out), "[warn]") {
			t.Errorf("nginx -t %s: %v\n%s", cfg, err, out)
		} else {
			t.Logf("nginx -t %s: %s", cfg, strings.TrimSpace(string(out)))
		}
	}
}

// writeTree writes files (slash paths relative to dir) for a container to
// mount read-only.
func writeTree(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()

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
}
