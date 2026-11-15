package core

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestInit(t *testing.T) {
	h := newHarness(t)
	res, err := h.eng.Init(h.ctx(), InitRequest{Dir: h.dir, Name: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SpecCreated || !res.StateCreated || !res.HomeCreated || !res.CACreated || res.Name != "lab" {
		t.Fatalf("result = %+v", res)
	}
	bb := filepath.Join(h.dir, ".bb")
	for path, want := range map[string]os.FileMode{
		bb:                                0o700,
		filepath.Join(bb, "secrets.json"): 0o600,
		h.eng.home.Dir:                    0o700,
		filepath.Join(h.eng.home.CADir(), "root.key"): 0o600,
	} {
		if got := perm(t, path); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	if !strings.Contains(h.read(".bb/.gitignore"), "\n*\n") {
		t.Error(".bb/.gitignore does not ignore everything")
	}
	if !strings.Contains(h.read("experiment.yaml"), "name: lab") {
		t.Error("experiment.yaml lacks the name")
	}

	// A clone has experiment.yaml but no .bb/: Init restores .bb only.
	if err := os.RemoveAll(bb); err != nil {
		t.Fatal(err)
	}
	res, err = h.eng.Init(h.ctx(), InitRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.SpecCreated || !res.StateCreated || res.CACreated || res.Name != "lab" {
		t.Fatalf("re-init result = %+v", res)
	}

	if _, err := h.eng.Init(h.ctx(), InitRequest{Dir: filepath.Join(t.TempDir(), "x"), Name: "Not Valid"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid name error = %v", err)
	}
	other, err := h.eng.Init(h.ctx(), InitRequest{Dir: filepath.Join(t.TempDir(), "My_Lab.v2")})
	if err != nil || other.Name != "my-lab-v2" {
		t.Fatalf("derived name = %+v %v", other, err)
	}
}

func TestOperationsNeedAnExperiment(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "kafka"}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("AddService error = %v", err)
	}
	if _, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Status error = %v", err)
	}
}

func TestCatalogAndLearn(t *testing.T) {
	h := newHarness(t)
	res, err := h.eng.Catalog(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range res.Entries {
		types = append(types, e.Type)
		if e.Image == "" || strings.HasSuffix(e.Image, ":latest") || !strings.Contains(e.Image, ":") {
			t.Errorf("%s image %q is not an exact pin", e.Type, e.Image)
		}
	}
	if !slices.Equal(types, []string{"kafka", "postgres", "redis", "consul"}) {
		t.Fatalf("types = %v", types)
	}
	l, err := h.eng.Learn(h.ctx(), LearnRequest{Type: "postgres"})
	if err != nil || !strings.Contains(l.Notes, "verify-full") {
		t.Fatalf("Learn = %+v %v", l, err)
	}
	for _, typ := range types {
		l, _ := h.eng.Learn(h.ctx(), LearnRequest{Type: typ})
		for _, section := range []string{"## What you got", "## Try this", "## Break this"} {
			if !strings.Contains(l.Notes, section) {
				t.Errorf("%s notes lack %q", typ, section)
			}
		}
	}
	if _, err := h.eng.Learn(h.ctx(), LearnRequest{Type: "mongodb"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown type error = %v", err)
	}
}

func TestAddAndRemovePreserveComments(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec(`version: 1
name: lab
# my services
services:
  kafka:
    type: kafka # the broker
apps:
  consumer:
    path: ./c # consumer code
    uses: [kafka]
`)
	res, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "postgres", Name: "db"})
	if err != nil || res.Name != "db" {
		t.Fatalf("AddService = %+v %v", res, err)
	}
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "kafka"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown type error = %v", err)
	}
	_, err = h.eng.RemoveService(h.ctx(), RemoveServiceRequest{Dir: h.dir, Name: "kafka"})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Violations[0].Path != "apps.consumer.uses[0]" {
		t.Fatalf("removing a used service: %v", err)
	}
	if r, err := h.eng.RemoveService(h.ctx(), RemoveServiceRequest{Dir: h.dir, Name: "consumer"}); err != nil || r.Kind != "app" {
		t.Fatalf("RemoveService = %+v %v", r, err)
	}
	if _, err := h.eng.RemoveService(h.ctx(), RemoveServiceRequest{Dir: h.dir, Name: "consumer"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove error = %v", err)
	}
	text := h.read("experiment.yaml")
	for _, want := range []string{"# my services", "# the broker", "db:\n    type: postgres"} {
		if !strings.Contains(text, want) {
			t.Errorf("experiment.yaml lacks %q:\n%s", want, text)
		}
	}
}

func TestApplyStarterIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.init()
	var events []Event
	res, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer", Events: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.AddedServices, []string{"kafka"}) || !slices.Equal(res.AddedApps, []string{"producer", "consumer"}) || len(res.Written) == 0 {
		t.Fatalf("first apply = %+v", res)
	}
	if !strings.Contains(h.read("producer-consumer/go.mod"), "go 1.27.4") {
		t.Error("go.mod does not target Go 1.27.4")
	}
	spec := h.read("experiment.yaml")
	if !strings.Contains(spec, "path: ./producer-consumer") || !strings.Contains(spec, "uses: [kafka]") {
		t.Errorf("experiment.yaml:\n%s", spec)
	}

	main := filepath.Join(h.dir, "producer-consumer", "cmd", "consumer", "main.go")
	if err := os.WriteFile(main, []byte("package main // mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer", Events: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 0 || len(res.AddedApps) != 0 || len(res.AddedServices) != 0 ||
		!slices.Equal(res.Conflicts, []string{"producer-consumer/cmd/consumer/main.go"}) ||
		!slices.Equal(res.ExistingApps, []string{"producer", "consumer"}) {
		t.Fatalf("second apply = %+v", res)
	}
	if data, _ := os.ReadFile(main); string(data) != "package main // mine\n" {
		t.Fatal("re-apply overwrote a user change")
	}
	if !slices.ContainsFunc(events, func(e Event) bool { return e.Kind == EventWarning && strings.Contains(e.Message, "main.go") }) {
		t.Error("no warning event for the conflict")
	}
	if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown starter error = %v", err)
	}
	if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer", Target: "../outside"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("target outside project error = %v", err)
	}
	for _, target := range []string{".bb/runtime", ".", "a:b"} {
		if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer", Target: target}); !errors.Is(err, ErrValidation) {
			t.Errorf("target %q error = %v", target, err)
		}
	}
	st, err := h.eng.Starters(h.ctx())
	if err != nil || len(st.Starters) == 0 || st.Starters[0].ID != "kafka/producer-consumer" {
		t.Fatalf("Starters = %+v %v", st, err)
	}
}

func setupStarter(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.init()
	if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer"}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestUp(t *testing.T) {
	h := setupStarter(t)
	h.add("postgres")
	var events []Event
	res, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir, Events: func(e Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Experiment != "lab" || len(res.Services) != 4 {
		t.Fatalf("result = %+v", res)
	}
	for _, s := range res.Services {
		if s.Health != HealthHealthy || s.MemoryBytes == 0 {
			t.Errorf("%s = %+v", s.Name, s)
		}
	}
	if !slices.Equal(h.rt.calls, []string{"available", "up"}) {
		t.Errorf("runtime calls = %v", h.rt.calls)
	}
	p := h.rt.projects[0]
	if p.Name != "bb-lab" || len(p.Files) != 2 {
		t.Fatalf("project = %+v", p)
	}
	compose := string(h.rt.filesSeen["docker-compose.yml"])
	for _, want := range []string{"image: apache/kafka:", "KAFKA_BROKERS: kafka.bb.internal:9093", "context: ../../producer-consumer", "breadboard.experiment: lab"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	if !strings.Contains(string(h.rt.filesSeen["docker-compose.dev.yml"]), "../../producer-consumer:/src") {
		t.Error("dev compose lacks the source bind mount")
	}
	if !slices.ContainsFunc(events, func(e Event) bool { return e.Kind == EventHealth && e.Service == "kafka" && e.Health == HealthHealthy }) {
		t.Error("no health event for kafka")
	}
	bundle := h.read(".bb/runtime/trust-bundle.pem")
	if !strings.Contains(bundle, "ROOTS") || !strings.HasPrefix(bundle, "-----BEGIN CERTIFICATE-----") {
		t.Error("trust bundle lacks the CA or the public roots")
	}

	// Every service has a certificate from the install CA, recorded in state.
	ca, _ := h.eng.CA(h.ctx())
	p2 := state.Project{Dir: h.dir}
	ps, err := p2.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"kafka", "postgres", "producer", "consumer"} {
		certPEM, _, ok, err := p2.ReadCert(svc)
		if !ok || err != nil {
			t.Fatalf("%s certificate missing: %v", svc, err)
		}
		cert, _ := pki.ParseCert(certPEM)
		if cert.DNSNames[0] != svc+".bb.internal" || cert.Issuer.CommonName != ca.Subject {
			t.Errorf("%s certificate = %v issued by %s", svc, cert.DNSNames, cert.Issuer.CommonName)
		}
		if ps.Certificates[svc].Issuer != "root" {
			t.Errorf("state does not record the issuer of %s", svc)
		}
	}
	sec, _ := p2.LoadSecrets()
	if pw, ok := sec.Get("postgres", "password"); !ok || len(pw) != 32 {
		t.Errorf("postgres password = %q", pw)
	}
}

func TestUpKeepsValidCertificatesAndReissuesNearExpiry(t *testing.T) {
	h := setupStarter(t)
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	p := state.Project{Dir: h.dir}
	first, _, _, _ := p.ReadCert("kafka")

	h.now = h.now.Add(59 * 24 * time.Hour) // 31 days left
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	second, _, _, _ := p.ReadCert("kafka")
	if string(first) != string(second) {
		t.Fatal("a certificate with 31 days left was reissued")
	}

	h.now = h.now.Add(2 * 24 * time.Hour) // 29 days left
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	third, _, _, _ := p.ReadCert("kafka")
	if string(second) == string(third) {
		t.Fatal("a certificate with 29 days left was not reissued")
	}
}

func TestUpFailures(t *testing.T) {
	t.Run("docker unavailable", func(t *testing.T) {
		h := setupStarter(t)
		h.rt.unavailable = ErrDockerUnavailable
		if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); !errors.Is(err, ErrDockerUnavailable) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unhealthy", func(t *testing.T) {
		h := setupStarter(t)
		h.rt.healthOf["consumer"] = HealthUnhealthy
		_, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
		if !errors.Is(err, ErrUnhealthy) || !strings.Contains(err.Error(), "bb logs consumer") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("exited", func(t *testing.T) {
		h := setupStarter(t)
		h.rt.stateOf["producer"] = "exited"
		if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); !errors.Is(err, ErrUnhealthy) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		h := setupStarter(t)
		h.rt.healthOf["kafka"] = HealthStarting
		_, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir, Timeout: 20 * time.Millisecond})
		if !errors.Is(err, ErrUnhealthy) || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("busy", func(t *testing.T) {
		h := setupStarter(t)
		lock, err := state.Project{Dir: h.dir}.Lock("up")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		_, err = h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
		if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "bb up is running") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("missing Dockerfile", func(t *testing.T) {
		h := setupStarter(t)
		if err := os.Remove(filepath.Join(h.dir, "producer-consumer", "Dockerfile")); err != nil {
			t.Fatal(err)
		}
		_, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Violations[0].Path != "apps.producer.path" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("newer state", func(t *testing.T) {
		h := setupStarter(t)
		if err := os.WriteFile(filepath.Join(h.dir, ".bb", "state.json"), []byte(`{"formatVersion": 7}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); !errors.Is(err, ErrNewerFormat) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPlaintextEdgeGetsAPrivateSubnet(t *testing.T) {
	h := setupStarter(t)
	h.add("postgres")
	spec := h.read("experiment.yaml") + "  pooler:\n    path: ./producer-consumer\n    uses:\n      - service: postgres\n        tls: {mode: off, reason: terminate TLS at a pooler}\n"
	h.writeSpec(spec)
	h.rt.subnets = []runtime.Network{{Subnet: netip.MustParsePrefix("10.213.0.0/24"), ExperimentID: "someone-else"}}
	var warnings []string
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir, Events: func(e Event) {
		if e.Kind == EventWarning {
			warnings = append(warnings, e.Message)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	ps, _ := state.Project{Dir: h.dir}.LoadState()
	if ps.Segments["plain-postgres"] != "10.213.1.0/24" {
		t.Fatalf("segments = %v, want the first free /24", ps.Segments)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "pooler -> postgres") {
		t.Errorf("warnings = %v", warnings)
	}
	// The allocation is stable while this experiment's own network holds it.
	h.rt.subnets = append(h.rt.subnets, runtime.Network{Subnet: netip.MustParsePrefix("10.213.1.0/24"), ExperimentID: ps.ExperimentID})
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	ps, _ = state.Project{Dir: h.dir}.LoadState()
	if ps.Segments["plain-postgres"] != "10.213.1.0/24" {
		t.Fatalf("segment moved to %v", ps.Segments)
	}
	// After a down, another experiment took the subnet: move to a free one.
	h.rt.subnets = []runtime.Network{
		{Subnet: netip.MustParsePrefix("10.213.0.0/24"), ExperimentID: "someone-else"},
		{Subnet: netip.MustParsePrefix("10.213.1.0/24"), ExperimentID: "someone-else"},
	}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	ps, _ = state.Project{Dir: h.dir}.LoadState()
	if ps.Segments["plain-postgres"] != "10.213.2.0/24" {
		t.Fatalf("segment = %v, want a fresh subnet after a conflict", ps.Segments)
	}
	st, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "plaintext edge pooler -> postgres:5432") {
		t.Errorf("status warnings = %v", st.Warnings)
	}
}

func TestSameNameInAnotherDirectoryIsRefused(t *testing.T) {
	h := setupStarter(t)
	h.rt.owners = []string{"another-experiment-id"}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "different directory") {
		t.Fatalf("Up error = %v", err)
	}
	if _, err := h.eng.Down(h.ctx(), DownRequest{Dir: h.dir, Volumes: true}); !errors.Is(err, ErrExists) {
		t.Fatalf("Down error = %v", err)
	}
	if slices.Contains(h.rt.calls, "up") || slices.Contains(h.rt.calls, "down-volumes") {
		t.Fatalf("runtime was driven anyway: %v", h.rt.calls)
	}
	ps, _ := state.Project{Dir: h.dir}.LoadState()
	h.rt.owners = []string{ps.ExperimentID}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatalf("Up with own resources: %v", err)
	}
	if !strings.Contains(string(h.rt.filesSeen["docker-compose.yml"]), "breadboard.experiment-id: "+ps.ExperimentID) {
		t.Error("compose lacks the experiment id label")
	}
}

func TestReissuedCertificateChangesTheConfigHash(t *testing.T) {
	h := setupStarter(t)
	hashOf := func() string {
		re := regexp.MustCompile(`(?m)breadboard.service: kafka\n\s+breadboard.kind: infra\n\s+breadboard.config-hash: (\w+)`)
		m := re.FindStringSubmatch(string(h.rt.filesSeen["docker-compose.yml"]))
		if m == nil {
			t.Fatalf("no config hash for kafka:\n%s", h.rt.filesSeen["docker-compose.yml"])
		}
		return m[1]
	}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	first := hashOf()
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	if hashOf() != first {
		t.Fatal("config hash changed without any change")
	}
	h.now = h.now.Add(65 * 24 * time.Hour)
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	if hashOf() == first {
		t.Fatal("a reissued certificate did not change the config hash, so compose would keep the old container")
	}
}

func TestDownStatusLogs(t *testing.T) {
	h := setupStarter(t)
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	st, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	if st.Domain != "bb.internal" || len(st.Services) != 3 || st.Services[0].Address != "kafka.bb.internal:9093" {
		t.Fatalf("status = %+v", st)
	}
	if st.Services[2].Name != "consumer" || len(st.Services[2].Uses) != 1 || !st.Services[2].Uses[0].TLS {
		t.Errorf("consumer = %+v", st.Services[2])
	}

	h.rt.logs = []runtime.LogLine{{Service: "consumer", Text: "consumed"}, {Service: "consumer", Text: "again"}}
	lines, err := h.eng.Logs(h.ctx(), LogsRequest{Dir: h.dir, Service: "consumer"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for l, err := range lines {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, l.Text)
	}
	if !slices.Equal(got, []string{"consumed", "again"}) || !slices.Equal(h.rt.logArgs, []string{"consumer"}) {
		t.Errorf("logs = %v args = %v", got, h.rt.logArgs)
	}
	if _, err := h.eng.Logs(h.ctx(), LogsRequest{Dir: h.dir, Service: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown service error = %v", err)
	}

	res, err := h.eng.Down(h.ctx(), DownRequest{Dir: h.dir, Volumes: true})
	if err != nil || !res.VolumesRemoved {
		t.Fatalf("Down = %+v %v", res, err)
	}
	if n := len(h.rt.calls); n < 2 || h.rt.calls[n-2] != "down-volumes" || h.rt.calls[n-1] != "remove-owned-volumes" {
		t.Errorf("calls = %v, want down-volumes then remove-owned-volumes", h.rt.calls)
	}
	ps, err := (state.Project{Dir: h.dir}).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if ps.ExperimentID == "" || !slices.Equal(h.rt.removedOwned, []string{ps.ExperimentID}) {
		t.Errorf("RemoveOwned ids = %v, want [%s]", h.rt.removedOwned, ps.ExperimentID)
	}
	if _, err := h.eng.Down(h.ctx(), DownRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	if h.rt.calls[len(h.rt.calls)-1] != "remove-owned" {
		t.Errorf("down without volumes must keep volumes: calls = %v", h.rt.calls)
	}
	st, _ = h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if st.Services[0].State != StateNotCreated {
		t.Errorf("after down: %+v", st.Services[0])
	}
}

func TestCreds(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("postgres")
	h.add("redis")
	res, err := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir, Service: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 1 || len(res.Services[0].Secrets) != 2 {
		t.Fatalf("creds = %+v", res)
	}
	pw := res.Services[0].Secrets[1].Value
	var url string
	for _, v := range res.Services[0].Env {
		if v.Name == "DATABASE_URL" {
			url = v.Value
		}
	}
	if url != "postgres://app:"+pw+"@postgres.bb.internal:5432/app?sslmode=verify-full" {
		t.Errorf("DATABASE_URL = %q", url)
	}
	again, _ := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir})
	if len(again.Services) != 2 || again.Services[0].Secrets[1].Value != pw {
		t.Error("secrets changed between calls")
	}
	if _, err := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir, Service: "kafka"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown service error = %v", err)
	}
}

func TestConfigAndDomainChange(t *testing.T) {
	h := setupStarter(t)
	got, err := h.eng.ConfigGet(h.ctx(), ConfigGetRequest{Key: "domain"})
	if err != nil || got.Settings[0].Value != "bb.internal" || got.Settings[0].Source != "default" {
		t.Fatalf("ConfigGet = %+v %v", got, err)
	}
	if _, err := h.eng.ConfigSet(h.ctx(), ConfigSetRequest{Key: "domain", Value: "Not A Domain"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid domain error = %v", err)
	}
	if _, err := h.eng.ConfigSet(h.ctx(), ConfigSetRequest{Key: "colour", Value: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key error = %v", err)
	}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	before, _ := h.eng.CA(h.ctx())

	set, err := h.eng.ConfigSet(h.ctx(), ConfigSetRequest{Key: "domain", Value: "lab.internal"})
	if err != nil || set.Setting.Value != "lab.internal" || set.OverriddenBy != "" {
		t.Fatalf("ConfigSet = %+v %v", set, err)
	}
	if perm(t, h.eng.home.ConfigPath()) != 0o600 {
		t.Error("config.yaml is not private")
	}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	after, _ := h.eng.CA(h.ctx())
	if after.Fingerprint == before.Fingerprint || after.Domain != "lab.internal" {
		t.Fatal("changing the domain did not mint a new CA")
	}
	certPEM, _, _, _ := state.Project{Dir: h.dir}.ReadCert("kafka")
	cert, _ := pki.ParseCert(certPEM)
	if cert.DNSNames[0] != "kafka.lab.internal" {
		t.Fatalf("kafka certificate names = %v", cert.DNSNames)
	}
	if !strings.Contains(string(h.rt.filesSeen["docker-compose.yml"]), "kafka.lab.internal") {
		t.Error("compose still uses the old domain")
	}

	t.Setenv("BB_DOMAIN", "env.internal")
	got, _ = h.eng.ConfigGet(h.ctx(), ConfigGetRequest{})
	if got.Settings[0].Value != "env.internal" || got.Settings[0].Source != "BB_DOMAIN" {
		t.Fatalf("env override = %+v", got.Settings)
	}
	set, _ = h.eng.ConfigSet(h.ctx(), ConfigSetRequest{Key: "domain", Value: "file.internal"})
	if set.OverriddenBy != "BB_DOMAIN" {
		t.Errorf("OverriddenBy = %q", set.OverriddenBy)
	}
}

func TestMigrate(t *testing.T) {
	h := newHarness(t)
	h.init()
	res, err := h.eng.Migrate(h.ctx(), MigrateRequest{Dir: h.dir})
	if err != nil || res.Changed || res.To != 1 {
		t.Fatalf("Migrate = %+v %v", res, err)
	}
	h.writeSpec("version: 9\nname: lab\n")
	if _, err := h.eng.Migrate(h.ctx(), MigrateRequest{Dir: h.dir}); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("newer spec error = %v", err)
	}
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "redis"}); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("AddService on a newer spec = %v", err)
	}
}

func TestExportCompose(t *testing.T) {
	h := setupStarter(t)
	out := filepath.Join(t.TempDir(), "stack")
	res, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out, Secrets: "dotenv"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Files, ".env") || !slices.Contains(res.Files, "README.md") {
		t.Fatalf("files = %v", res.Files)
	}
	compose, _ := os.ReadFile(filepath.Join(out, "docker-compose.yml"))
	if !strings.Contains(string(compose), "docker compose -f docker-compose.yml") {
		t.Error("export header lacks the run command")
	}
	rel, _ := filepath.Rel(out, filepath.Join(h.dir, "producer-consumer"))
	if !strings.Contains(string(compose), "context: "+filepath.ToSlash(rel)) {
		t.Errorf("app context is not relative to the export:\n%s", compose)
	}
	ignore, _ := os.ReadFile(filepath.Join(out, ".gitignore"))
	if !strings.Contains(string(ignore), ".env") {
		t.Error(".gitignore lacks .env")
	}
	if h.rt.calls != nil && slices.Contains(h.rt.calls, "up") {
		t.Error("export started containers")
	}

	// Re-export replaces bb's own directory; a foreign one is refused.
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out}); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".env")); !os.IsNotExist(err) {
		t.Error("inline re-export left the old .env behind")
	}
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: foreign}); !errors.Is(err, ErrExists) {
		t.Fatalf("foreign dir error = %v", err)
	}
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: h.dir}); !errors.Is(err, ErrValidation) {
		t.Fatalf("project dir error = %v", err)
	}
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Secrets: "vault"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad secrets mode error = %v", err)
	}
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	if v := h.eng.Version(); v.Version != "v0.0.0-test" {
		t.Fatalf("Version = %+v", v)
	}
}

func TestContextCancellationStopsWaiting(t *testing.T) {
	h := setupStarter(t)
	h.rt.healthOf["kafka"] = HealthStarting
	h.eng.healthTimeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.eng.Up(ctx, UpRequest{Dir: h.dir}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
