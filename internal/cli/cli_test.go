package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
)

func run(t *testing.T, eng *core.Engine, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), eng, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func engine(t *testing.T) *core.Engine {
	t.Helper()
	os.Unsetenv("BB_DOMAIN")
	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Version: "v9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestParseAllowsFlagsAfterArguments(t *testing.T) {
	fs := newFlagSet("logs")
	follow := fs.Bool("f", false, "")
	tail := fs.Int("tail", 0, "")
	pos, err := parse(fs, []string{"consumer", "-f", "--tail", "5"})
	if err != nil || len(pos) != 1 || pos[0] != "consumer" || !*follow || *tail != 5 {
		t.Fatalf("parse = %v %v follow=%v tail=%d", pos, err, *follow, *tail)
	}
}

func TestProjectCommands(t *testing.T) {
	eng := engine(t)
	dir := filepath.Join(t.TempDir(), "lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, eng, "-C", dir, "init", "kafka-lab")
	if code != 0 || !strings.Contains(out, "Created experiment kafka-lab") || !strings.Contains(out, "Created .bb/") {
		t.Fatalf("init: %d %q", code, out)
	}
	if code, out, _ = run(t, eng, "-C", dir, "add", "kafka"); code != 0 || !strings.Contains(out, "Added kafka") {
		t.Fatalf("add: %d %q", code, out)
	}
	if code, out, _ = run(t, eng, "-C", dir, "starter", "kafka/producer-consumer"); code != 0 || !strings.Contains(out, "added apps: producer, consumer") {
		t.Fatalf("starter: %d %q", code, out)
	}
	code, _, errOut := run(t, eng, "-C", dir, "remove", "kafka")
	if code != 1 || !strings.Contains(errOut, "apps.producer.uses[0]") {
		t.Fatalf("remove used service: %d %q", code, errOut)
	}
	if code, out, _ = run(t, eng, "-C", dir, "creds", "kafka"); code != 0 || !strings.Contains(out, "KAFKA_BROKERS=kafka.bb.internal:9093") {
		t.Fatalf("creds: %d %q", code, out)
	}
	if code, out, _ = run(t, eng, "-C", dir, "migrate"); code != 0 || !strings.Contains(out, "already at version 1") {
		t.Fatalf("migrate: %d %q", code, out)
	}
}

func TestInformationCommands(t *testing.T) {
	eng := engine(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"version"}, "bb v9.9.9"},
		{[]string{"catalog"}, "apache/kafka:"},
		{[]string{"starters"}, "kafka/producer-consumer"},
		{[]string{"learn", "redis"}, "## Break this"},
		{[]string{"ca"}, "Constrained to"},
		{[]string{"config"}, "domain"},
		{[]string{"config", "get", "domain"}, "bb.internal"},
		{[]string{"help", "logs"}, "usage: bb logs"},
		{nil, "commands:"},
	} {
		code, out, errOut := run(t, eng, tc.args...)
		if code != 0 || !strings.Contains(out, tc.want) {
			t.Errorf("bb %v = %d %q %q, want output containing %q", tc.args, code, out, errOut, tc.want)
		}
	}
	code, out, _ := run(t, eng, "ca", "--pem")
	if code != 0 || !strings.HasPrefix(out, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("ca --pem = %d %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	eng := engine(t)
	for _, args := range [][]string{{"nope"}, {"add"}, {"logs", "a", "b"}, {"export", "k8s"}, {"config", "set", "domain"}, {"up", "--bogus"}} {
		if code, _, errOut := run(t, eng, args...); code != 2 || errOut == "" {
			t.Errorf("bb %v = %d %q, want usage error", args, code, errOut)
		}
	}
	code, _, errOut := run(t, eng, "-C", t.TempDir(), "status")
	if code != 1 || !strings.Contains(errOut, "bb init") {
		t.Errorf("status outside an experiment = %d %q", code, errOut)
	}
}

func TestStatusRowsNestSidecarsAndReplicas(t *testing.T) {
	services := []core.ServiceStatus{
		{Name: "postgres", Kind: "infra", Health: "healthy", Address: "postgres.bb.internal:5432"},
		{Name: "web-1", Kind: "app", Health: "healthy", Address: "web-1.bb.internal", App: "web", Instance: 1, Replicas: 2},
		{Name: "web-1-envoy", Kind: "sidecar", Type: "envoy", Health: "healthy", Parent: "web-1", Sidecar: &core.SidecarInfo{}},
		{Name: "web-2", Kind: "app", Health: "starting", Address: "web-2.bb.internal", App: "web", Instance: 2, Replicas: 2},
		{Name: "web-2-envoy", Kind: "sidecar", Type: "envoy", Health: "healthy", Parent: "web-2", Sidecar: &core.SidecarInfo{}},
		{Name: "caller", Kind: "app", Health: "healthy", App: "caller", Replicas: 1,
			Uses: []core.Edge{{To: "postgres", TLS: true}, {To: "web", TLS: true, Via: "envoy", ToApp: true}}},
		{Name: "caller-envoy", Kind: "sidecar", Type: "envoy", Health: "healthy", Parent: "caller",
			Sidecar: &core.SidecarInfo{Targets: []string{"web"}, Transparent: true, Egress: "deny"}},
		{Name: "caller-agent", Kind: "sidecar", Type: "consul-agent", Health: "healthy", Parent: "caller", Registry: "consul",
			Sidecar: &core.SidecarInfo{Targets: []string{"consul"}}},
	}
	var got []string
	for _, r := range serviceRows(services) {
		got = append(got, r[0]+"|"+r[1]+"|"+r[2]+"|"+r[6])
	}
	want := []string{
		"postgres|infra|healthy|-",
		"web|app ×2|-|-",
		"  web-1|app|healthy|-",
		"    └ web-1-envoy|envoy|healthy|-",
		"  web-2|app|starting|-",
		"    └ web-2-envoy|envoy|healthy|-",
		"caller|app|healthy|postgres, web (via envoy)",
		"  └ caller-envoy|envoy|healthy|web, transparent, egress deny",
		"  └ caller-agent|consul-agent|healthy|registers in consul",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A proxy's row lists its upstream edges with mode and target port.
func TestStatusRowsShowProxyUpstreamModes(t *testing.T) {
	services := []core.ServiceStatus{
		{Name: "edge", Kind: "infra", Health: "healthy", Uses: []core.Edge{
			{To: "web", Port: 8443, TLS: true, ToApp: true, Mode: "on"},
			{To: "api", Port: 8080, ToApp: true, Mode: "off", Reason: "practice"},
			{To: "payments", Port: 8443, TLS: true, ToApp: true, Mode: "passthrough"},
		}},
		{Name: "worker", Kind: "app", Health: "healthy", Uses: []core.Edge{{To: "postgres", Port: 5432, Reason: "pooler"}}},
	}
	rows := serviceRows(services)
	if got := rows[0][6]; got != "web (TLS :8443), api (terminate :8080, PLAINTEXT), payments (passthrough :8443)" {
		t.Errorf("edge uses = %q", got)
	}
	if got := rows[1][6]; got != "postgres (PLAINTEXT)" {
		t.Errorf("worker uses = %q", got)
	}
}
