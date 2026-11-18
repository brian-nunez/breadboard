package sidecar

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

// TestEnvoySidecarConfigValidates runs envoy --mode validate inside the
// pinned sidecar image on generated sidecar configs (inbound and outbound
// with every option, transparent deny and allow), with a real certificate
// from a lab CA. Any deprecation warning fails it (D-162, D-203). It needs
// Docker.
func TestEnvoySidecarConfigValidates(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}

	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available")
	}

	t.Parallel()

	retries := 0
	configs := map[string]Input{
		"outbound.yaml": envoyInput(spec.EnvoySidecar{
			Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080},
			Outbound: []spec.EnvoyOutbound{
				{Service: "orders", Port: 9001, LB: "maglev", Timeout: "1m30s", Outlier: &spec.Outlier{Consecutive5xx: 5, Ejection: "30s", MaxEjectionPercent: 50, Interval: "5s"}},
				{Service: "s3", Port: 9002, Retries: &retries},
			},
		},
			Target{Service: "orders", App: true, FQDN: "orders.bb.internal", Port: 8443},
			Target{Service: "s3", FQDN: "s3.bb.internal", Port: 8333},
		),
		"deny.yaml":  transparentInput("deny"),
		"allow.yaml": transparentInput("allow"),
	}
	typ, _ := Lookup(spec.SidecarEnvoy)
	files := map[string][]byte{}

	for name, in := range configs {
		out, err := typ.RenderSidecar(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		files["config/"+name] = out.Files[0].Data
	}

	dir := sidecarLabFiles(t, files)
	for name := range configs {
		out, err := exec.Command("docker", "run", "--rm", "--network", "none", "-v", dir+":/etc/bb:ro",
			typ.Image, "envoy", "--mode", "validate", "-c", "/etc/bb/config/"+name).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "configuration '/etc/bb/config/"+name+"' OK") {
			t.Errorf("envoy --mode validate %s: %v\n%s", name, err, out)
		}

		if strings.Contains(strings.ToLower(string(out)), "deprecated") {
			t.Errorf("envoy --mode validate %s warns about a deprecated field (D-162, D-203):\n%s", name, out)
		}
	}
}

// sidecarLabFiles writes a lab CA, a certificate for api.bb.internal and
// the given files under a fresh directory laid out like /etc/bb.
func sidecarLabFiles(t *testing.T, files map[string][]byte) string {
	t.Helper()

	now := time.Now()

	ca, err := pki.NewCA("bb.internal", now)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := ca.Issue(pki.LeafRequest{DNSNames: []string{"api.bb.internal"}, Identity: pki.Identity("bb.internal", "lab", "api")}, now)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	all := map[string][]byte{"tls/tls.crt": leaf.CertPEM, "tls/tls.key": leaf.KeyPEM, "ca.pem": ca.CertPEM}
	for p, data := range files {
		all[p] = data
	}

	for p, data := range all {
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

	return dir
}
