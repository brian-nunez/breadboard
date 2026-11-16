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

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

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
	if p.Name != "bb-lab" || len(p.Files) != 1 {
		t.Fatalf("project = %+v", p)
	}
	compose := string(h.rt.filesSeen["docker-compose.yml"])
	for _, want := range []string{"image: apache/kafka:", "KAFKA_BROKERS: kafka.bb.internal:9093", "context: ../../producer-consumer", "breadboard.experiment: lab"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose lacks %q", want)
		}
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
	// A container that crashes and comes back between polls still looks
	// running and healthy; its restart count and start time give it away.
	for name, healthy := range map[string]bool{"before healthy": false, "while settling": true} {
		t.Run("restarted "+name, func(t *testing.T) {
			h := setupStarter(t)
			if !healthy {
				h.rt.healthOf["consumer"] = HealthStarting
			}
			// Poll 1 records the baseline (and, when all are healthy, starts
			// the settle window); poll 3 sees the restart.
			h.rt.restartOnPoll = map[string]int{"consumer": 3}
			_, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
			if !errors.Is(err, ErrUnhealthy) || !strings.Contains(err.Error(), "consumer restarted during startup") || !strings.Contains(err.Error(), "bb logs consumer") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("restarting", func(t *testing.T) {
		h := setupStarter(t)
		h.rt.stateOf["producer"] = "restarting"
		if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); !errors.Is(err, ErrUnhealthy) || !strings.Contains(err.Error(), "producer is restarting") {
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
	checkExperimentIDLabels(t, h.rt.filesSeen["docker-compose.yml"], ps.ExperimentID)
}

// The experiment id label holds the id whatever it looks like: compose
// quotes an id YAML would read as a number, so the check decodes the YAML
// instead of matching text. The ids are 16 lowercase hex digits.
func TestExperimentIDLabelOfAnyShape(t *testing.T) {
	for name, id := range map[string]string{
		"all digits":     "1234567890123456",
		"leading zeros":  "0012345678901234",
		"exponent shape": "1e10000000000000",
		"ordinary":       "3fa9c0d2e17b4b68",
	} {
		t.Run(name, func(t *testing.T) {
			h := setupStarter(t)
			if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
				t.Fatal(err)
			}
			p := state.Project{Dir: h.dir}
			ps, err := p.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			ps.ExperimentID = id
			if err := p.SaveState(ps, h.eng.version); err != nil {
				t.Fatal(err)
			}
			h.rt.owners = []string{id}
			if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
				t.Fatal(err)
			}
			checkExperimentIDLabels(t, h.rt.filesSeen["docker-compose.yml"], id)
		})
	}
}

// checkExperimentIDLabels decodes a compose file and checks that every
// service, network and volume carries the experiment id label with id.
func checkExperimentIDLabels(t *testing.T, data []byte, id string) {
	t.Helper()
	type labelled struct {
		Labels map[string]string `yaml:"labels"`
	}
	var doc struct {
		Services map[string]labelled `yaml:"services"`
		Networks map[string]labelled `yaml:"networks"`
		Volumes  map[string]labelled `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("compose is not YAML: %v", err)
	}
	if len(doc.Services) == 0 {
		t.Fatalf("compose has no services:\n%s", data)
	}
	for kind, items := range map[string]map[string]labelled{"service": doc.Services, "network": doc.Networks, "volume": doc.Volumes} {
		for name, item := range items {
			if got, ok := item.Labels["breadboard.experiment-id"]; !ok || got != id {
				t.Errorf("%s %s: breadboard.experiment-id = %q (set %v), want %q", kind, name, got, ok, id)
			}
		}
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
	if c := st.Services[2]; c.Path != "./producer-consumer" || !slices.Equal(c.Command, []string{"consumer"}) {
		t.Errorf("consumer source = %q %v", c.Path, c.Command)
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
