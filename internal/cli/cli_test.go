package cli

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
	"github.com/brian-nunez/breadboard/internal/runtime"
)

func run(t *testing.T, eng *core.Engine, args ...string) (int, string, string) {
	t.Helper()

	var out, errOut bytes.Buffer

	code := Run(context.Background(), eng, args, &out, &errOut)

	return code, out.String(), errOut.String()
}

func engine(t *testing.T) *core.Engine {
	t.Helper()

	_ = os.Unsetenv("BB_DOMAIN")

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

func TestCAShowsBothCAs(t *testing.T) {
	eng := engine(t)

	res, err := eng.CA(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run(t, eng, "ca")
	if code != 0 {
		t.Fatalf("bb ca = %d %q", code, errOut)
	}

	for _, want := range []string{
		"Lab CA (root)", res.Subject, "*.bb.internal", res.Fingerprint, res.CertPath,
		"Host CA (host)", "Breadboard host CA (localhost)", "*.localhost", res.Host.Fingerprint, res.Host.CertPath,
		"https://<service>.<experiment>.localhost:<port>",
		"security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db",
		"update-ca-certificates", "trust anchor", "Firefox", "curl --resolve", "/etc/hosts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bb ca output lacks %q:\n%s", want, out)
		}
	}

	if strings.Index(out, "Lab CA") > strings.Index(out, "Host CA") {
		t.Error("bb ca shows the host CA before the lab CA")
	}

	code, out, _ = run(t, eng, "ca", "--host")
	if code != 0 || strings.Contains(out, "Lab CA") || !strings.Contains(out, res.Host.Fingerprint) {
		t.Errorf("bb ca --host = %d %q", code, out)
	}

	code, out, _ = run(t, eng, "ca", "--pem")
	if code != 0 || out != string(res.CertPEM) {
		t.Errorf("bb ca --pem = %d %q, want the lab CA only", code, out)
	}

	code, out, _ = run(t, eng, "ca", "--host", "--pem")
	if code != 0 || out != string(res.Host.CertPEM) {
		t.Errorf("bb ca --host --pem = %d %q, want the host CA only", code, out)
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
		{
			Name: "caller", Kind: "app", Health: "healthy", App: "caller", Replicas: 1,
			Uses: []core.Edge{{To: "postgres", TLS: true}, {To: "web", TLS: true, Via: "envoy", ToApp: true}},
		},
		{
			Name: "caller-envoy", Kind: "sidecar", Type: "envoy", Health: "healthy", Parent: "caller",
			Sidecar: &core.SidecarInfo{Targets: []string{"web"}, Transparent: true, Egress: "deny"},
		},
		{
			Name: "caller-agent", Kind: "sidecar", Type: "consul-agent", Health: "healthy", Parent: "caller", Registry: "consul",
			Sidecar: &core.SidecarInfo{Targets: []string{"consul"}},
		},
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

// A Traefik that discovers routes in Consul lists where they come from,
// worded like the registration agent's row.
func TestStatusRowsShowDiscoveryEdges(t *testing.T) {
	rows := serviceRows([]core.ServiceStatus{{Name: "edge", Kind: "infra", Health: "healthy", Uses: []core.Edge{
		{To: "consul", Port: 8501, TLS: true, Discovery: "registry"},
		{To: "web", Port: 8443, TLS: true, ToApp: true, Discovery: "route"},
	}}})
	if got := rows[0][6]; got != "discovers in consul, routes to web" {
		t.Errorf("edge uses = %q", got)
	}
}

func TestPrintHostURLs(t *testing.T) {
	var out strings.Builder

	c := &cli{out: &out}
	c.printHostURLs(nil)

	if out.Len() != 0 {
		t.Errorf("no URLs printed %q", out.String())
	}

	c.printHostURLs([]core.HostURL{
		{URL: "https://api.lab.localhost:44312", Target: "api"},
		{URL: "https://api-2.lab.localhost:44312", Target: "api", Instance: "api-2"},
	})

	want := "\nHost access (trust the host CA: bb ca):\n" +
		"  https://api.lab.localhost:44312    → api\n" +
		"  https://api-2.lab.localhost:44312  → api-2 (one instance of api)\n"
	if out.String() != want {
		t.Errorf("printed\n%q\nwant\n%q", out.String(), want)
	}
}

// bb add app <name> scaffolds the learner's program; "app" is reserved, so
// no catalog type may take it.
func TestAddApp(t *testing.T) {
	eng := engine(t)

	res, err := eng.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range res.Entries {
		if e.Type == appWord {
			t.Fatalf("catalog type %q collides with bb add %s", e.Type, appWord)
		}
	}

	dir := filepath.Join(t.TempDir(), "lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"init", "lab"}, {"add", "postgres"}, {"add", "redis"}} {
		if code, out, errOut := run(t, eng, append([]string{"-C", dir}, args...)...); code != 0 {
			t.Fatalf("%v: %d %q %q", args, code, out, errOut)
		}
	}

	code, out, errOut := run(t, eng, "-C", dir, "add", "app", "shop")
	if code != 0 {
		t.Fatalf("add app: %d %q", code, errOut)
	}

	for _, want := range []string{
		"Added app shop in ./shop", "wrote shop/main.go", "wrote shop/Dockerfile",
		"postgres", "Postgres", "DATABASE_URL (secret), PGHOST", "REDIS_URL (secret)",
		"bb up", "bb creds", "./shop/main.go is empty", "bb up shows it exited",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("add app output lacks %q:\n%s", want, out)
		}

		if strings.Contains(out, "bb logs") {
			t.Errorf("add app output points at bb logs, but the empty program logs nothing:\n%s", out)
		}
	}

	if code, out, _ = run(t, eng, "-C", dir, "add", "app", "cache", "--uses", "redis", "--dir", "apps/cache"); code != 0 || !strings.Contains(out, "./apps/cache") || strings.Contains(out, "Postgres") {
		t.Fatalf("add app --uses --dir: %d %q", code, out)
	}

	if code, out, _ = run(t, eng, "-C", dir, "add", "app", "solo", "--uses="); code != 0 || !strings.Contains(out, "uses no services yet") {
		t.Fatalf("add app --uses '': %d %q", code, out)
	}

	data, err := os.ReadFile(filepath.Join(dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"uses: [postgres, redis]", "path: ./apps/cache", "uses: [redis]", "command: [solo]"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("experiment.yaml lacks %q:\n%s", want, data)
		}
	}

	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"add", "app"}, 2, "expected an app name"},
		{[]string{"add", "app", "a", "b"}, 2, "expected an app name"},
		{[]string{"add", "--name", "x", "app"}, 2, "put app first"},
		{[]string{"add", "app", "shop"}, 1, "a service or app named \"shop\" already exists in experiment.yaml; choose another name\n"},
		{[]string{"add", "app", "web", "--uses", "mongo"}, 1, "uses[0]: mongo is not a service"},
		{[]string{"add", "app", "web", "--dir", "shop"}, 1, "already exists and has files"},
	} {
		code, out, errOut := run(t, eng, append([]string{"-C", dir}, tc.args...)...)
		if code != tc.code || !strings.Contains(out+errOut, tc.want) {
			t.Errorf("bb %v = %d %q %q, want %d containing %q", tc.args, code, out, errOut, tc.code, tc.want)
		}
	}
	// A bad request reads as one sentence, not as an invalid experiment.yaml.
	if _, _, errOut := run(t, eng, "-C", dir, "add", "app", "web", "--uses", "mongo"); errOut != "bb: uses[0]: mongo is not a service in experiment.yaml; add it with bb add <type> first\n" {
		t.Errorf("bad --uses: %q", errOut)
	}

	if code, out, _ := run(t, eng, "help", "add"); code != 0 || !strings.Contains(out, "app <name> [--uses a,b] [--dir path]") {
		t.Errorf("help add = %d %q", code, out)
	}
}

// downRuntime is a Docker that runs nothing and records the volumes
// argument of each Down.
type downRuntime struct{ volumes []bool }

func (*downRuntime) Available(context.Context) error           { return nil }
func (*downRuntime) Up(context.Context, runtime.Project) error { return nil }
func (r *downRuntime) Down(_ context.Context, _ runtime.Project, volumes bool) error {
	r.volumes = append(r.volumes, volumes)

	return nil
}
func (*downRuntime) RemoveOwned(context.Context, string, bool) error        { return nil }
func (*downRuntime) Subnets(context.Context) ([]runtime.Network, error)     { return nil, nil }
func (*downRuntime) Build(context.Context, runtime.Project, []string) error { return nil }
func (*downRuntime) ImageUser(context.Context, runtime.Project, string) (runtime.ImageUser, error) {
	return runtime.ImageUser{}, nil
}
func (*downRuntime) Owners(context.Context, string) ([]string, error) { return nil, nil }
func (*downRuntime) PublishedPorts(context.Context) ([]runtime.PublishedPort, error) {
	return nil, nil
}

func (*downRuntime) Exec(context.Context, string, string, []string) ([]byte, error) {
	return nil, errors.New("not running")
}

func (*downRuntime) Containers(context.Context, string) ([]runtime.Container, error) {
	return nil, nil
}

func (*downRuntime) Stats(context.Context, []string) (map[string]runtime.Usage, error) {
	return map[string]runtime.Usage{}, nil
}

func (*downRuntime) StatsStream(context.Context) iter.Seq2[runtime.Sample, error] {
	return func(func(runtime.Sample, error) bool) {}
}

func (*downRuntime) Logs(context.Context, runtime.Project, []string, bool, int) iter.Seq2[runtime.LogLine, error] {
	return func(func(runtime.LogLine, error) bool) {}
}

func TestDownDeletesVolumesByDefault(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		volumes bool
		out     string
	}{
		{nil, true, "its data volumes are deleted"},
		{[]string{"--keep-volumes"}, false, "data volumes are kept"},
		{[]string{"--volumes"}, true, "its data volumes are deleted"},
	} {
		_ = os.Unsetenv("BB_DOMAIN")

		rt := &downRuntime{}

		eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: rt, Version: "v9.9.9"})
		if err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		if code, out, errOut := run(t, eng, "-C", dir, "init", "lab"); code != 0 {
			t.Fatalf("init: %d %q %q", code, out, errOut)
		}

		code, out, errOut := run(t, eng, append([]string{"-C", dir, "down"}, tc.args...)...)
		if code != 0 || !strings.Contains(out, tc.out) {
			t.Fatalf("bb down %v = %d %q %q, want %q", tc.args, code, out, errOut, tc.out)
		}

		if !slices.Equal(rt.volumes, []bool{tc.volumes}) {
			t.Errorf("bb down %v: DownRequest.Volumes = %v, want %v", tc.args, rt.volumes, tc.volumes)
		}
	}
}

func TestDownRejectsKeepVolumesWithVolumes(t *testing.T) {
	rt := &downRuntime{}

	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: rt, Version: "v9.9.9"})
	if err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run(t, eng, "-C", t.TempDir(), "down", "--keep-volumes", "--volumes")
	if code != 2 || !strings.Contains(errOut, "usage: bb down [--keep-volumes]") {
		t.Fatalf("bb down --keep-volumes --volumes = %d %q, want usage error", code, errOut)
	}

	if len(rt.volumes) != 0 {
		t.Errorf("Down ran with volumes %v", rt.volumes)
	}
}

func TestDownHelpHidesVolumes(t *testing.T) {
	code, out, _ := run(t, engine(t), "help", "down")
	if code != 0 || !strings.Contains(out, "bb down [--keep-volumes]") || strings.Contains(out, "--volumes") {
		t.Fatalf("help down = %d %q", code, out)
	}
}
