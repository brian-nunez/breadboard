package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func envNames(u AppUse) []string {
	var names []string
	for _, v := range u.Env {
		names = append(names, v.Name)
	}

	return names
}

func TestAddApp(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		t.Fatal(err)
	}

	h.writeSpec(`# my lab
version: 1
name: lab
services:
  postgres:
    type: postgres # the database
  redis:
    type: redis
`)
	h.init()

	res, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}

	if res.Name != "shop" || res.Dir != "./shop" || !slices.Equal(res.Command, []string{"shop"}) {
		t.Fatalf("result = %+v", res)
	}

	wantFiles := []string{"shop/Dockerfile", "shop/README.md", "shop/go.mod", "shop/go.sum", "shop/main.go"}
	got := slices.Clone(res.Files)
	slices.Sort(got)

	if !slices.Equal(got, wantFiles) {
		t.Fatalf("files = %v, want %v", got, wantFiles)
	}
	// Default uses: every infra service, in spec order.
	if len(res.Uses) != 2 || res.Uses[0].Service != "postgres" || res.Uses[1].Service != "redis" || res.Uses[0].Title != "Postgres" || res.Uses[1].Type != "redis" {
		t.Fatalf("uses = %+v", res.Uses)
	}

	if names := envNames(res.Uses[0]); !slices.Equal(names, []string{"DATABASE_URL", "PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE", "PGSSLROOTCERT"}) {
		t.Errorf("postgres env = %v", names)
	}

	for _, u := range res.Uses {
		for _, v := range u.Env {
			if v.Secret && v.Value != "" {
				t.Errorf("%s %s: secret value returned", u.Service, v.Name)
			}
		}
	}

	if v := res.Uses[0].Env[1]; v.Name != "PGHOST" || v.Value != "postgres.bb.internal" {
		t.Errorf("PGHOST = %+v", v)
	}

	spec := h.read("experiment.yaml")
	for _, want := range []string{"# my lab", "# the database", "  shop:\n    path: ./shop\n    command: [shop]\n    uses: [postgres, redis]\n"} {
		if !strings.Contains(spec, want) {
			t.Errorf("experiment.yaml lacks %q:\n%s", want, spec)
		}
	}

	main := h.read("shop/main.go")
	for _, want := range []string{`"DATABASE_URL", "PGHOST"`, `"REDIS_URL",`, `_ "github.com/jackc/pgx/v5/pgxpool"`, `_ "github.com/redis/go-redis/v9"`, "requireEnv(", "postgres.bb.internal", "\nfunc main() {\n}\n"} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go lacks %q", want)
		}
	}

	if !strings.Contains(h.read("shop/Dockerfile"), "AS dev") {
		t.Error("Dockerfile has no dev stage")
	}
	// The project is valid for bb up: the app has a Dockerfile with a dev stage.
	p, err := h.eng.open(h.dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := p.checkApps(); err != nil {
		t.Errorf("checkApps: %v", err)
	}
}

func TestAddAppUses(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("postgres")
	h.add("redis")
	h.add("kafka")

	res, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "only-redis", Uses: []string{"redis"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Uses) != 1 || res.Uses[0].Service != "redis" {
		t.Fatalf("uses = %+v", res.Uses)
	}

	if !strings.Contains(h.read("experiment.yaml"), "  only-redis:\n    path: ./only-redis\n    command: [only-redis]\n    uses: [redis]\n") {
		t.Errorf("experiment.yaml:\n%s", h.read("experiment.yaml"))
	}

	if code := h.read("only-redis/main.go"); strings.Contains(code, "pgxpool") || strings.Contains(code, "kgo") {
		t.Error("main.go imports clients of services the app does not use")
	}

	// An empty list means no services; --path puts the code elsewhere.
	res, err = h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "solo", Uses: []string{}, Path: "apps/solo"})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Uses) != 0 || res.Dir != "./apps/solo" {
		t.Fatalf("result = %+v", res)
	}

	if s := h.read("experiment.yaml"); !strings.Contains(s, "  solo:\n    path: ./apps/solo\n    command: [solo]\n") {
		t.Errorf("experiment.yaml:\n%s", s)
	}

	if !strings.Contains(h.read("apps/solo/main.go"), "No services yet") {
		t.Error("main.go for no services does not say so")
	}

	before := h.read("experiment.yaml")
	for _, tc := range []struct {
		uses []string
		want string
	}{
		{[]string{"redis", "mongo"}, "uses[1]: mongo is not a service in experiment.yaml"},
		{[]string{"redis", "redis"}, "uses[1]: redis is listed twice"},
		{[]string{"solo"}, "uses[0]: solo is an app"},
	} {
		_, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "bad", Uses: tc.uses})
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("uses %v: error = %v, want %q", tc.uses, err, tc.want)
		}
	}

	if _, err := os.Stat(filepath.Join(h.dir, "bad")); !os.IsNotExist(err) {
		t.Error("a refused app left its directory")
	}

	if h.read("experiment.yaml") != before {
		t.Error("a refused app changed experiment.yaml")
	}
}

func TestAddAppRefusals(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("redis")

	if _, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "api"}); err != nil {
		t.Fatal(err)
	}
	// A directory with files is the learner's: never touched.
	work := filepath.Join(h.dir, "mine")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main // mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := h.read("experiment.yaml")
	for _, tc := range []struct {
		req  AddAppRequest
		is   error
		want string
	}{
		{AddAppRequest{Name: "web", Path: "mine"}, ErrExists, "already exists and has files"},
		{AddAppRequest{Name: "mine"}, ErrExists, "already exists and has files"},
		{AddAppRequest{Name: "api", Path: "api2"}, ErrExists, `named "api"`},
		{AddAppRequest{Name: "redis"}, ErrExists, `named "redis"`},
		{AddAppRequest{Name: "bb-host"}, ErrValidation, "reserved for the host router"},
		{AddAppRequest{Name: "Web_App"}, ErrValidation, "lowercase DNS label"},
		{AddAppRequest{Name: ""}, ErrValidation, "lowercase DNS label"},
		{AddAppRequest{Name: "web", Path: "../outside"}, ErrValidation, "inside the project"},
		{AddAppRequest{Name: "web", Path: ".bb/x"}, ErrValidation, "outside .bb"},
		{AddAppRequest{Name: "web", Path: "."}, ErrValidation, "new directory"},
	} {
		tc.req.Dir = h.dir

		_, err := h.eng.AddApp(h.ctx(), tc.req)
		if !errors.Is(err, tc.is) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: error = %v, want %v containing %q", tc.req, err, tc.is, tc.want)
		}
	}

	if got := h.read("mine/main.go"); got != "package main // mine\n" {
		t.Errorf("the learner's file changed: %q", got)
	}

	if entries, _ := os.ReadDir(work); len(entries) != 1 {
		t.Errorf("the learner's directory gained files: %v", entries)
	}

	if after := h.read("experiment.yaml"); after != before {
		t.Errorf("experiment.yaml after refusals:\n%s", after)
	}

	// A symlink out of the project is refused, and nothing is written there.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.dir, "escape")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"escape/web", "escape"} {
		if _, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "web", Path: path}); err == nil {
			t.Errorf("path %s through a symlink out of the project: no error", path)
		}
	}

	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("AddApp wrote outside the project: %v", entries)
	}

	// A symlink inside the project to .bb is refused too: the code would
	// land in disposable generated state.
	if err := os.Symlink(".bb", filepath.Join(h.dir, "alias")); err != nil {
		t.Fatal(err)
	}

	yaml := h.read("experiment.yaml")
	for _, path := range []string{"alias/inside", "alias", "alias/a/b"} {
		_, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "inside", Path: path})
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "leads into .bb") {
			t.Errorf("path %s into .bb: error = %v", path, err)
		}
	}

	for _, p := range []string{".bb/inside", ".bb/a"} {
		if _, err := os.Stat(filepath.Join(h.dir, p)); !os.IsNotExist(err) {
			t.Errorf("AddApp wrote %s", p)
		}
	}

	if h.read("experiment.yaml") != yaml {
		t.Error("a refused app changed experiment.yaml")
	}

	// An existing empty directory is fine.
	if err := os.MkdirAll(filepath.Join(h.dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "empty"}); err != nil {
		t.Fatalf("empty directory: %v", err)
	}
}

// Two services of one type: bb up prefixes the later one's variables, and
// the scaffold names them exactly so.
func TestAddAppPrefixedEnv(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("postgres")

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "postgres", Name: "analytics"}); err != nil {
		t.Fatal(err)
	}

	res, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "report"})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Uses) != 2 {
		t.Fatalf("uses = %+v", res.Uses)
	}

	if names := envNames(res.Uses[1]); names[0] != "ANALYTICS_DATABASE_URL" || names[1] != "ANALYTICS_PGHOST" {
		t.Errorf("analytics env = %v", names)
	}

	code := h.read("report/main.go")
	for _, want := range []string{`"ANALYTICS_DATABASE_URL", "ANALYTICS_PGHOST"`, `_ "github.com/jackc/pgx/v5/pgxpool" // postgres, analytics`} {
		if !strings.Contains(code, want) {
			t.Errorf("main.go lacks %q", want)
		}
	}

	// The names match what ServiceDetails (and so bb up) reports.
	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "report"})
	if err != nil {
		t.Fatal(err)
	}

	for i, g := range d.Env[1:] {
		var names []string
		for _, v := range g.Vars {
			names = append(names, v.Name)
		}

		if !slices.Equal(names, envNames(res.Uses[i])) {
			t.Errorf("%s: details %v, AddApp %v", g.Service, names, envNames(res.Uses[i]))
		}
	}
}

func TestAddAppNeedsExperiment(t *testing.T) {
	h := newHarness(t)
	if _, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "x"}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("error = %v", err)
	}
}
