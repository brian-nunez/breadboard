package compose

import (
	"bytes"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/catalog/sidecar"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

var update = flag.Bool("update", false, "rewrite golden files")

func allServices() []spec.Service {
	return []spec.Service{
		{Name: "kafka", Type: "kafka"},
		{Name: "postgres", Type: "postgres", Config: map[string]string{"database": "orders"}},
		{Name: "redis", Type: "redis", Resources: spec.Resources{Memory: "128m"}},
		{Name: "consul", Type: "consul"},
		{Name: "s3", Type: "s3-compatible", Config: map[string]string{"buckets": "uploads,reports"}},
	}
}

func uses(names ...string) []spec.Use {
	out := make([]spec.Use, len(names))
	for i, n := range names {
		out[i] = spec.Use{Service: n}
	}
	return out
}

func render(t *testing.T, s *spec.Spec, mode secrets.Mode) *Bundle {
	t.Helper()
	m, err := model.Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	return renderModel(t, m, mode)
}

func renderModel(t *testing.T, m *model.Model, mode secrets.Mode) *Bundle {
	t.Helper()
	return renderModelWith(t, m, mode, nil)
}

func renderModelWith(t *testing.T, m *model.Model, mode secrets.Mode, readFile func(string) ([]byte, error)) *Bundle {
	t.Helper()
	b, err := renderModelErr(m, mode, readFile)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderModelErr(m *model.Model, mode secrets.Mode, readFile func(string) ([]byte, error)) (*Bundle, error) {
	// Fixed secrets and certificates keep the output stable.
	store := &state.Secrets{}
	for _, svc := range m.Services {
		if svc.Kind != model.Infra {
			continue
		}
		for _, sec := range svc.Entry.Secrets {
			store.Set(svc.Name, sec.Key, fmt.Sprintf("%s%sSECRET", svc.Name, sec.Key))
		}
	}
	certs := map[string]Cert{}
	for _, svc := range m.Services {
		certs[svc.Name] = Cert{Cert: []byte("CERT " + svc.Name + "\n"), Key: []byte("KEY " + svc.Name + "\n")}
	}
	subnets := map[string]netip.Prefix{}
	for i, seg := range m.Segments {
		subnets[seg.Name] = netip.MustParsePrefix(fmt.Sprintf("10.213.%d.0/24", i))
	}
	// A lab with a transparent pod gets static addressing (core allocates
	// it from state; here it follows the model's order).
	var addrs map[string]netip.Addr
	if slices.ContainsFunc(m.Services, func(s *model.Service) bool { return s.Transparent }) {
		subnets[DefaultNetwork] = netip.MustParsePrefix("10.213.200.0/24")
		addrs = map[string]netip.Addr{}
		next := netip.MustParseAddr("10.213.200.10")
		for _, svc := range m.Services {
			if svc.Kind == model.Sidecar {
				continue
			}
			addrs[networkOwner(svc)] = next
			next = next.Next()
		}
	}
	return Render(Input{
		Model:        m,
		ExperimentID: "0123456789abcdef",
		Secrets:      secrets.NewMaterializer(mode, store),
		CA:           []byte("CA\n"),
		TrustBundle:  []byte("CA\nROOTS\n"),
		Certs:        certs,
		Subnets:      subnets,
		Addresses:    addrs,
		AppDir:       func(app string) string { return "../../" + app },
		ReadFile:     readFile,
		Header:       "Generated for a golden test.",
	})
}

func golden(t *testing.T, name string, b *Bundle) {
	t.Helper()
	var buf bytes.Buffer
	for _, f := range b.Files {
		// Certificate material is summarised; everything else is compared in full.
		if f.Path == "ca.pem" || f.Path == "trust-bundle.pem" || strings.HasPrefix(f.Path, "tls/") {
			fmt.Fprintf(&buf, "--- %s (%o) %d bytes\n", f.Path, f.Mode, len(f.Data))
			continue
		}
		fmt.Fprintf(&buf, "--- %s (%o)\n%s", f.Path, f.Mode, f.Data)
	}
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/compose -update)", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("%s differs from %s; review the diff and rerun with -update if intended\n%s", name, path, buf.String())
	}
}

func checkCompose(t *testing.T, b *Bundle) {
	t.Helper()
	for _, name := range []string{ComposeFile} {
		f, ok := b.File(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(f.Data, &doc); err != nil {
			t.Fatalf("%s is not valid YAML: %v", name, err)
		}
	}
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	for name, svc := range doc.Services {
		if svc["restart"] == "no" {
			// A one-shot init container: no health check, but something
			// must wait for it to complete successfully.
			waited := false
			for _, other := range doc.Services {
				if deps, ok := other["depends_on"].(map[string]any); ok {
					if d, ok := deps[name].(map[string]any); ok && d["condition"] == "service_completed_successfully" {
						waited = true
					}
				}
			}
			for _, key := range []string{"mem_limit", "cpus", "labels"} {
				if _, ok := svc[key]; !ok {
					t.Errorf("init container %s has no %s", name, key)
				}
			}
			if !waited {
				t.Errorf("nothing waits for init container %s to complete", name)
			}
			continue
		}
		for _, key := range []string{"healthcheck", "mem_limit", "cpus", "restart", "labels"} {
			if _, ok := svc[key]; !ok {
				t.Errorf("service %s has no %s", name, key)
			}
		}
		if svc["restart"] != "unless-stopped" {
			t.Errorf("service %s restart = %v", name, svc["restart"])
		}
	}
}

func TestEveryEntryInline(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Services: allServices(), Apps: []spec.App{
		{Name: "api", Path: "./api", Command: []string{"go", "run", "./cmd/api"}, Uses: uses("kafka", "postgres", "redis", "consul", "s3"), Ports: []int{8080}, Health: &spec.Health{Path: "/ready"}},
	}}
	b := render(t, s, secrets.Inline)
	checkCompose(t, b)
	if _, ok := b.File(EnvFile); ok {
		t.Error("inline mode wrote a .env file")
	}
	golden(t, "every-entry-inline", b)
}

func TestEveryEntryDotenv(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Services: allServices(), Apps: []spec.App{
		{Name: "api", Path: "./api", Command: []string{"go", "run", "./cmd/api"}, Uses: uses("kafka", "postgres", "redis", "consul", "s3")},
	}}
	b := render(t, s, secrets.Dotenv)
	checkCompose(t, b)
	f, _ := b.File(ComposeFile)
	// Test secret values end in SECRET right after a lowercase letter
	// (postgrespasswordSECRET); variable names such as AWS_SECRET_ACCESS_KEY do not.
	if regexp.MustCompile(`[a-z0-9]SECRET`).Match(f.Data) {
		t.Error("dotenv mode left a secret value in docker-compose.yml")
	}
	env, ok := b.File(EnvFile)
	if !ok || env.Mode != 0o600 || !bytes.Contains(env.Data, []byte("POSTGRES__PASSWORD=postgrespasswordSECRET")) {
		t.Errorf(".env = %q (mode %o)", env.Data, env.Mode)
	}
	golden(t, "every-entry-dotenv", b)
}

func TestPlaintextEdges(t *testing.T) {
	off := func(svc, reason string) spec.Use {
		return spec.Use{Service: svc, TLS: spec.TLS{Mode: spec.TLSOff, Reason: reason}}
	}
	s := &spec.Spec{Version: 1, Name: "lab", Services: allServices(), Apps: []spec.App{
		{Name: "pooler", Path: "./pooler", Uses: []spec.Use{off("postgres", "practice TLS termination at a pooler")}},
		{Name: "api", Path: "./api", Uses: []spec.Use{{Service: "postgres"}, off("kafka", "compare throughput"), off("consul", "debug with curl")}},
		{Name: "worker", Path: "./worker", Uses: uses("redis")},
	}}
	b := render(t, s, secrets.Inline)
	checkCompose(t, b)
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]struct {
			Networks map[string]any `yaml:"networks"`
		} `yaml:"services"`
		Networks map[string]map[string]any `yaml:"networks"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	// Only the target and the clients that asked for plaintext share a segment.
	members := map[string][]string{}
	for name, svc := range doc.Services {
		for n := range svc.Networks {
			members[n] = append(members[n], name)
		}
	}
	for seg, want := range map[string][]string{
		"plain-postgres": {"pooler", "postgres"},
		"plain-kafka":    {"api", "kafka"},
		"plain-consul":   {"api", "consul"},
	} {
		got := members[seg]
		if !sameSet(got, want) {
			t.Errorf("%s members = %v, want %v", seg, got, want)
		}
		if doc.Networks[seg]["internal"] != true {
			t.Errorf("%s is not internal", seg)
		}
	}
	if _, ok := members["plain-redis"]; ok {
		t.Error("redis got a plaintext segment without a plaintext edge")
	}
	hba, _ := b.File("config/postgres/pg_hba.conf")
	if !bytes.Contains(hba.Data, []byte("hostnossl all      all  10.213.")) || !bytes.Contains(hba.Data, []byte("Reason: pooler: practice TLS termination")) {
		t.Errorf("pg_hba.conf lacks the segment rule or reason:\n%s", hba.Data)
	}
	golden(t, "plaintext-edges", b)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestSecondServiceOfSameTypeGetsPrefixedEnv(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Services: []spec.Service{{Name: "orders", Type: "postgres"}, {Name: "analytics", Type: "postgres"}}, Apps: []spec.App{
		{Name: "api", Path: "./api", Uses: uses("orders", "analytics")},
	}}
	b := render(t, s, secrets.Inline)
	f, _ := b.File(ComposeFile)
	for _, want := range []string{"DATABASE_URL: postgres://app:orderspasswordSECRET@orders.bb.internal", "ANALYTICS_DATABASE_URL: postgres://app:analyticspasswordSECRET@analytics.bb.internal"} {
		if !bytes.Contains(f.Data, []byte(want)) {
			t.Errorf("compose lacks %q", want)
		}
	}
}

func TestDollarSignsInUserInputAreEscaped(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Apps: []spec.App{{Name: "api", Path: "./api", Command: []string{"sh", "-c", "echo $HOME"}}}}
	b := render(t, s, secrets.Inline)
	f, _ := b.File(ComposeFile)
	if !bytes.Contains(f.Data, []byte("echo $$HOME")) {
		t.Errorf("dollar sign not escaped:\n%s", f.Data)
	}
}

func TestSegmentAddress(t *testing.T) {
	if got := SegmentAddress(netip.MustParsePrefix("10.213.4.0/24")).String(); got != "10.213.4.10" {
		t.Fatalf("SegmentAddress = %s", got)
	}
}

func TestRelPath(t *testing.T) {
	for _, tc := range []struct{ base, target, want string }{
		{"/p/.bb/runtime", "/p/orders", "../../orders"},
		{"/p/export/compose", "/p/export/compose/app", "./app"},
		{"/out", "/p/app", "../p/app"},
	} {
		got, err := RelPath(tc.base, tc.target)
		if err != nil || got != tc.want {
			t.Errorf("RelPath(%s, %s) = %q %v, want %q", tc.base, tc.target, got, err, tc.want)
		}
	}
}

// Apps run binaries compiled into their images, so the bundle has one
// compose file: no source mounts or Go cache volumes.
func TestBundleHasOneComposeFile(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Services: allServices(), Apps: []spec.App{
		{Name: "producer", Path: "./orders", Uses: uses("kafka")},
	}}
	b := render(t, s, secrets.Inline)
	if _, ok := b.File("docker-compose.dev.yml"); ok {
		t.Error("the bundle still has a dev compose file")
	}
	f, _ := b.File(ComposeFile)
	for _, gone := range []string{"go-mod-cache", "go-build-cache", ":/src"} {
		if strings.Contains(string(f.Data), gone) {
			t.Errorf("docker-compose.yml still mentions %s", gone)
		}
	}
}

// A registering app gets a sidecar agent that shares its network namespace
// and certificate, waits for the registry, and registers the app.
func TestRegisterRendersASidecar(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "consul", Type: "consul"}},
		Apps:     []spec.App{{Name: "greeter", Path: "./greeter", Command: []string{"go", "run", "."}, Uses: uses("consul"), Register: &spec.Register{Port: 8443}}},
	}
	b := render(t, s, secrets.Dotenv)
	checkCompose(t, b)
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]struct {
			NetworkMode string         `yaml:"network_mode"`
			Networks    map[string]any `yaml:"networks"`
			Volumes     []string       `yaml:"volumes"`
			DependsOn   map[string]struct {
				Condition string `yaml:"condition"`
			} `yaml:"depends_on"`
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	sc, ok := doc.Services["greeter-agent"]
	if !ok {
		t.Fatal("no greeter-agent service")
	}
	if sc.NetworkMode != "service:greeter" || sc.Networks != nil {
		t.Errorf("sidecar networking = %q %v", sc.NetworkMode, sc.Networks)
	}
	if sc.DependsOn["greeter"].Condition != "service_started" || sc.DependsOn["consul"].Condition != "service_healthy" {
		t.Errorf("depends_on = %+v", sc.DependsOn)
	}
	if !slices.Contains(sc.Volumes, "./tls/greeter/tls.crt:/etc/bb/tls/tls.crt:ro") {
		t.Errorf("sidecar does not mount the app's certificate: %v", sc.Volumes)
	}
	if sc.Labels["breadboard.kind"] != "sidecar" {
		t.Errorf("labels = %v", sc.Labels)
	}
	if _, ok := b.File("tls/greeter-agent/tls.crt"); ok {
		t.Error("the sidecar got a certificate of its own")
	}
	cfg, ok := b.File("config/greeter-agent/consul.json")
	if !ok {
		t.Fatal("no sidecar config")
	}
	for _, want := range []string{`"retry_join": [`, `"consul.bb.internal"`, `"name": "greeter"`, `"port": 8443`, `"tcp_use_tls": true`, `"client_addr": "127.0.0.1"`, `"leave_on_terminate": true`} {
		if !strings.Contains(string(cfg.Data), want) {
			t.Errorf("sidecar config lacks %s:\n%s", want, cfg.Data)
		}
	}
	golden(t, "consul-sidecar", b)
}

// Replicas render as named instances, each answering to its own name and
// to the app's shared name, each with its own registration agent.
func TestReplicasRenderNamedInstances(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "postgres", Type: "postgres"}, {Name: "consul", Type: "consul"}},
		Apps: []spec.App{{Name: "api", Path: "./api", Command: []string{"api"}, Replicas: 3,
			Uses: uses("postgres", "consul"), Register: &spec.Register{Port: 8443}}},
	}
	b := render(t, s, secrets.Inline)
	checkCompose(t, b)
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]struct {
			Build    map[string]string `yaml:"build"`
			Networks map[string]struct {
				Aliases []string `yaml:"aliases"`
			} `yaml:"networks"`
			NetworkMode string            `yaml:"network_mode"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Services["api"]; ok {
		t.Error("an app with replicas still has a service under its own name")
	}
	for _, n := range []string{"1", "2", "3"} {
		inst, ok := doc.Services["api-"+n]
		if !ok {
			t.Fatalf("no api-%s", n)
		}
		want := []string{"api-" + n + ".bb.internal", "api.bb.internal"}
		if got := inst.Networks["default"].Aliases; !slices.Equal(got, want) {
			t.Errorf("api-%s aliases = %v, want %v", n, got, want)
		}
		if inst.Build["context"] != "../../api" || inst.Environment["BB_SERVICE"] != "api-"+n || inst.Environment["BB_FQDN"] != "api-"+n+".bb.internal" {
			t.Errorf("api-%s = %+v", n, inst)
		}
		if agent := doc.Services["api-"+n+"-agent"]; agent.NetworkMode != "service:api-"+n {
			t.Errorf("api-%s-agent network_mode = %q", n, agent.NetworkMode)
		}
	}
	golden(t, "replicas", b)
}

// fakeSidecar stands in for a sidecar type in pod rendering tests: it
// exercises every part of the rendering hook.
func fakeSidecar() *sidecar.Type {
	return &sidecar.Type{
		ID: "fake", Title: "Fake", Image: "example/fake:1.0.0",
		Resources: catalog.Resources{Memory: "32m", CPUs: "0.1"},
		Render: func(in sidecar.Input) (sidecar.Output, error) {
			var pg sidecar.Use
			for _, u := range in.Uses {
				if u.Type == "postgres" {
					pg = u
				}
			}
			out := sidecar.Output{
				Container: catalog.Container{
					Command:     []string{"fake", "--config", catalog.ConfigDir + "/fake.conf"},
					Environment: []catalog.EnvVar{{Name: "FAKE_APP", Value: in.App.FQDN}},
					Files:       []catalog.File{{Name: "fake.conf", Data: []byte("app " + in.App.Service + " shared " + in.App.SharedFQDN + "\n")}},
					Health:      catalog.Health{Test: []string{"CMD", "fake", "check"}, Interval: 10 * time.Second, Timeout: 2 * time.Second, Retries: 3, StartPeriod: 5 * time.Second},
				},
				AppEnv: []catalog.EnvVar{
					{Name: "BB_LISTEN_ADDR", Value: "127.0.0.1:8080"},
					{Name: "DATABASE_URL", Value: "postgres://pooled@127.0.0.1:6432/" + pg.Config["database"] + "?sslmode=disable", Secret: true},
				},
				Needs:    []string{pg.Service},
				Rewrites: pg.Service,
			}
			if in.Spec.Transparent() {
				out.Init = &sidecar.Init{
					Dockerfile: []byte("FROM " + sidecar.PodImage + "\nRUN apk add --no-cache iptables\n"),
					Command:    []string{"sh", "-c", "iptables -t nat -S"},
					CapAdd:     []string{"NET_ADMIN"},
				}
			}
			return out, nil
		},
	}
}

// podModel resolves a spec and swaps every declared sidecar's type for the fake.
func podModel(t *testing.T, s *spec.Spec) *model.Model {
	t.Helper()
	m, err := model.Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	fake := fakeSidecar()
	for _, svc := range m.Services {
		if svc.Sidecar != nil {
			svc.SidecarType, svc.Image, svc.Resources = fake, fake.Image, fake.Resources
		}
	}
	return m
}

// An app with sidecars becomes a pod: the holder owns the network, the
// app and its sidecars join its namespace, and start in order holder →
// init → sidecars → app.
func TestPodRendersAHolderAndStartOrder(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "postgres", Type: "postgres"}, {Name: "consul", Type: "consul"}, {Name: "kafka", Type: "kafka"}},
		Apps: []spec.App{{Name: "web", Path: "./web", Command: []string{"web"}, Ports: []int{8443},
			Uses:     []spec.Use{{Service: "postgres"}, {Service: "consul"}, {Service: "kafka", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "compare throughput"}}},
			Register: &spec.Register{Port: 8443},
			Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{Transparent: &spec.Transparent{}}}},
		}},
	}
	b := renderModel(t, podModel(t, s), secrets.Inline)
	checkCompose(t, b)
	f, _ := b.File(ComposeFile)
	type dep struct {
		Condition string `yaml:"condition"`
	}
	var doc struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			NetworkMode string            `yaml:"network_mode"`
			Networks    map[string]any    `yaml:"networks"`
			Ports       []string          `yaml:"ports"`
			Volumes     []string          `yaml:"volumes"`
			CapAdd      []string          `yaml:"cap_add"`
			Restart     string            `yaml:"restart"`
			DependsOn   map[string]dep    `yaml:"depends_on"`
			Environment map[string]string `yaml:"environment"`
			Labels      map[string]string `yaml:"labels"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	holder := doc.Services["web-pod"]
	if holder.Image != sidecar.PodImage || !slices.Equal(holder.Ports, []string{"127.0.0.1:8443:8443"}) || holder.Networks["plain-kafka"] == nil || holder.Labels["breadboard.kind"] != "pod" {
		t.Errorf("holder = %+v", holder)
	}
	app := doc.Services["web"]
	if app.NetworkMode != "service:web-pod" || app.Networks != nil || app.Ports != nil {
		t.Errorf("app networking = %q %v %v", app.NetworkMode, app.Networks, app.Ports)
	}
	for name, cond := range map[string]string{"web-pod": "service_healthy", "web-netinit": "service_completed_successfully", "postgres": "service_healthy", "web-envoy": "service_healthy", "web-agent": "service_healthy"} {
		if app.DependsOn[name].Condition != cond {
			t.Errorf("app depends_on %s = %q, want %s", name, app.DependsOn[name].Condition, cond)
		}
	}
	if app.Environment["BB_SHARED_DIR"] != sidecar.SharedDir || app.Environment["BB_LISTEN_ADDR"] != "127.0.0.1:8080" || app.Environment["DATABASE_URL"] != "postgres://pooled@127.0.0.1:6432/app?sslmode=disable" {
		t.Errorf("app env = %v", app.Environment)
	}
	for _, name := range []string{"web-envoy", "web-agent"} {
		sc := doc.Services[name]
		if sc.NetworkMode != "service:web-pod" || sc.DependsOn["web-pod"].Condition != "service_healthy" || sc.DependsOn["web-netinit"].Condition != "service_completed_successfully" {
			t.Errorf("%s = %+v", name, sc)
		}
		if !slices.Contains(sc.Volumes, "web-shared:/var/run/bb") || !slices.Contains(sc.Volumes, "./tls/web/tls.crt:/etc/bb/tls/tls.crt:ro") {
			t.Errorf("%s volumes = %v", name, sc.Volumes)
		}
		if len(sc.CapAdd) > 0 {
			t.Errorf("%s holds capabilities %v", name, sc.CapAdd)
		}
	}
	if doc.Services["web-envoy"].DependsOn["postgres"].Condition != "service_healthy" {
		t.Error("the sidecar does not wait for what it needs")
	}
	init := doc.Services["web-netinit"]
	if init.NetworkMode != "service:web-pod" || init.Restart != "no" || !slices.Equal(init.CapAdd, []string{"NET_ADMIN"}) || init.DependsOn["web-pod"].Condition != "service_healthy" {
		t.Errorf("netinit = %+v", init)
	}
	if !slices.Contains(app.Volumes, "web-shared:/var/run/bb") || doc.Volumes["web-shared"] == nil {
		t.Errorf("shared volume missing: app %v, volumes %v", app.Volumes, doc.Volumes)
	}
	if _, ok := b.File("build/web-netinit/Dockerfile"); !ok {
		t.Error("no netinit Dockerfile")
	}
	if _, ok := b.File("tls/web-pod/tls.crt"); ok {
		t.Error("the pod holder got a certificate")
	}
	golden(t, "pod", b)
}

// With replicas, each instance is its own pod.
func TestReplicasOfAPodAreEachAPod(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "postgres", Type: "postgres"}},
		Apps: []spec.App{{Name: "api", Path: "./api", Replicas: 2, Uses: uses("postgres"),
			Sidecars: []spec.Sidecar{{Type: "pgbouncer", PgBouncer: &spec.PgBouncerSidecar{Service: "postgres"}}}}},
	}
	b := renderModel(t, podModel(t, s), secrets.Inline)
	checkCompose(t, b)
	f, _ := b.File(ComposeFile)
	var doc struct {
		Services map[string]struct {
			NetworkMode string `yaml:"network_mode"`
			Networks    map[string]struct {
				Aliases []string `yaml:"aliases"`
			} `yaml:"networks"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"1", "2"} {
		inst := "api-" + n
		if got := doc.Services[inst+"-pod"].Networks["default"].Aliases; !slices.Equal(got, []string{inst + ".bb.internal", "api.bb.internal"}) {
			t.Errorf("%s-pod aliases = %v", inst, got)
		}
		if _, ok := doc.Services[inst+"-netinit"]; ok {
			t.Errorf("%s has an init container without transparent mode", inst)
		}
		for _, c := range []string{inst, inst + "-pgbouncer"} {
			if doc.Services[c].NetworkMode != "service:"+inst+"-pod" {
				t.Errorf("%s network_mode = %q", c, doc.Services[c].NetworkMode)
			}
		}
	}
}
