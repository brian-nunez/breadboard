package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/starters"
)

// Every sidecar starter applies to one experiment (their app names are
// unique), the result validates and renders, and sidecar references to
// infra resolve to the experiment's own service names, like uses.
func TestSidecarStartersApplyAndRender(t *testing.T) {
	h := newHarness(t)
	h.init()
	// An existing Postgres under another name: the PgBouncer sidecar must
	// point at it, not add a second one.
	h.writeSpec("version: 1\nname: lab\nservices:\n  db:\n    type: postgres\n")

	var ids []string

	for _, s := range starters.All() {
		if strings.HasPrefix(s.ID, "sidecar/") {
			ids = append(ids, s.ID)
		}
	}

	if len(ids) != 6 {
		t.Fatalf("sidecar starters = %v", ids)
	}

	for _, id := range ids {
		if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: id}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}

	spec := h.read("experiment.yaml")
	for _, want := range []string{
		"replicas: 3",
		"inbound: {port: 8443, app_port: 8080}",
		"service: ambassador-backend",
		"transparent: {egress: deny}",
		"outlier: {consecutive_5xx: 3, ejection: 30s, max_ejection_percent: 50, interval: 5s}",
		"type: pgbouncer\n        service: db\n",
		"type: fluent-bit\n        to: s3-compatible\n        bucket: app-logs\n",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("experiment.yaml lacks %q:\n%s", want, spec)
		}
	}

	if strings.Contains(spec, "  postgres:\n    type: postgres") {
		t.Errorf("a second Postgres was added:\n%s", spec)
	}

	out := filepath.Join(t.TempDir(), "export")
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out}); err != nil {
		t.Fatal(err)
	}

	compose, err := os.ReadFile(filepath.Join(out, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{
		"ambassador-backend-3-envoy:", "ambassador-caller-envoy:", "plain-web-envoy:",
		"transparent-caller-netinit:", "breaker-backend-1-envoy:", "pool-client-pgbouncer:", "log-writer-fluent-bit:",
		"AMBASSADOR_BACKEND_URL: http://127.0.0.1:9001", "BREAKER_BACKEND_URL: http://127.0.0.1:9001", "BB_LOG_DIR: /var/run/bb/logs",
	} {
		if !strings.Contains(string(compose), svc) {
			t.Errorf("docker-compose.yml lacks %q", svc)
		}
	}
}
