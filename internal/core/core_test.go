package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

	if !slices.Equal(types, []string{"kafka", "postgres", "redis", "consul", "s3-compatible", "haproxy", "envoy", "traefik", "nginx", "pgbouncer"}) {
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

// A name the starter needs that belongs to something else is a clash: the
// apply fails before writing anything, instead of skipping the app.
func TestApplyStarterRefusesAnAppNameClash(t *testing.T) {
	for name, src := range map[string]string{
		"app elsewhere": "version: 1\nname: lab\napps:\n  producer:\n    path: ./mine\n",
		"infra service": "version: 1\nname: lab\nservices:\n  consumer:\n    type: redis\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.init()

			if name == "app elsewhere" {
				if err := os.MkdirAll(filepath.Join(h.dir, "mine"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			h.writeSpec(src)
			before := h.read("experiment.yaml")

			_, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer"})
			if !errors.Is(err, ErrExists) {
				t.Fatalf("error = %v, want ErrExists", err)
			}

			if h.read("experiment.yaml") != before {
				t.Error("experiment.yaml changed despite the clash")
			}

			if _, err := os.Stat(filepath.Join(h.dir, "producer-consumer")); !os.IsNotExist(err) {
				t.Error("starter files were written despite the clash")
			}
		})
	}
}

func setupStarter(t *testing.T) *harness {
	t.Helper()

	return withStarter(newHarness(t))
}

// withStarter initialises h's project with kafka/producer-consumer.
func withStarter(h *harness) *harness {
	t := h.t
	t.Helper()
	h.init()

	if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "kafka/producer-consumer"}); err != nil {
		t.Fatal(err)
	}

	return h
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

func TestVersion(t *testing.T) {
	h := newHarness(t)
	if v := h.eng.Version(); v.Version != "v0.0.0-test" {
		t.Fatalf("Version = %+v", v)
	}
}

func TestStarterDetailsWritesNothing(t *testing.T) {
	h := newHarness(t)

	d, err := h.eng.StarterDetails(h.ctx(), StarterDetailsRequest{ID: "consul/service-discovery"})
	if err != nil {
		t.Fatal(err)
	}

	if d.Dir != "service-discovery" || len(d.Apps) != 2 || d.Apps[0].Register == nil || d.Apps[0].Register.Port != 8443 {
		t.Fatalf("details = %+v", d)
	}

	paths := map[string]bool{}
	for _, f := range d.Files {
		paths[f.Path] = true
	}

	for _, want := range []string{"go.mod", "Dockerfile", "README.md", "cmd/greeter/main.go", "cmd/caller/main.go"} {
		if !paths[want] {
			t.Errorf("files lack %s", want)
		}
	}

	if _, err := os.Stat(h.dir); !os.IsNotExist(err) {
		t.Error("StarterDetails created the project directory")
	}

	if _, err := h.eng.StarterDetails(h.ctx(), StarterDetailsRequest{ID: "nope/nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown starter error = %v", err)
	}
}

// bb add pgbouncer lists the experiment's only Postgres as its upstream, so
// the new pool validates without editing experiment.yaml; with no Postgres
// the bare service is refused and validation says what to list.
func TestAddPgBouncerWiresTheOnlyPostgres(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec("version: 1\nname: lab\nservices:\n  db:\n    type: postgres\n")

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "pgbouncer", Name: "pool"}); err != nil {
		t.Fatalf("AddService pgbouncer: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(h.dir, "experiment.yaml"))
	if err != nil || !strings.Contains(string(data), "pool:\n    type: pgbouncer\n    upstreams:\n      - service: db\n") {
		t.Fatalf("experiment.yaml = %s %v", data, err)
	}

	h2 := newHarness(t)
	h2.init()
	h2.writeSpec("version: 1\nname: lab\nservices: {}\n")

	if _, err := h2.eng.AddService(h2.ctx(), AddServiceRequest{Dir: h2.dir, Type: "pgbouncer"}); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "upstreams") {
		t.Fatalf("AddService pgbouncer without postgres = %v", err)
	}
}
