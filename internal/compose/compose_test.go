package compose

import (
	"bytes"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

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
	b, err := Render(Input{
		Model:        m,
		ExperimentID: "0123456789abcdef",
		Secrets:      secrets.NewMaterializer(mode, store),
		CA:           []byte("CA\n"),
		TrustBundle:  []byte("CA\nROOTS\n"),
		Certs:        certs,
		Subnets:      subnets,
		AppDir:       func(app string) string { return "../../" + app },
		Header:       "Generated for a golden test.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
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
	for _, name := range []string{ComposeFile, DevComposeFile} {
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

// Apps start together and share the Go cache volumes. Without nocopy, Docker
// copies the image's module cache into the new volume once per container at
// the same time, and the losers fail with "mkdir ...: file exists".
func TestSharedGoCachesAreNotCopiedFromTheImage(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Services: allServices(), Apps: []spec.App{
		{Name: "producer", Path: "./orders", Uses: uses("kafka")},
		{Name: "consumer", Path: "./orders", Uses: uses("kafka")},
	}}
	b := render(t, s, secrets.Inline)
	f, ok := b.File(DevComposeFile)
	if !ok {
		t.Fatal("no dev compose file")
	}
	var doc struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"producer", "consumer"} {
		caches := 0
		for _, v := range doc.Services[app].Volumes {
			m, ok := v.(map[string]any)
			if !ok {
				if str, _ := v.(string); strings.Contains(str, "cache") {
					t.Errorf("%s mounts %q in short form, which cannot set nocopy", app, str)
				}
				continue
			}
			caches++
			opts, _ := m["volume"].(map[string]any)
			if m["type"] != "volume" || opts["nocopy"] != true {
				t.Errorf("%s cache mount %v lacks volume.nocopy", app, m)
			}
		}
		if caches != 2 {
			t.Errorf("%s has %d Go cache mounts, want 2", app, caches)
		}
	}
}
