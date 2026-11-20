package spec

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// types is a small stand-in for the catalog.
func types(id string) (TypeInfo, bool) {
	switch id {
	case "kafka":
		return TypeInfo{Plaintext: true}, true
	case "redis":
		return TypeInfo{}, true
	case "consul":
		return TypeInfo{Registry: true}, true
	case "postgres":
		return TypeInfo{Plaintext: true, Config: func(k, v string) error {
			if k != "database" {
				return errors.New("unknown option")
			}

			if v == "" {
				return errors.New("must not be empty")
			}

			return nil
		}}, true
	}

	return TypeInfo{}, false
}

const example = `version: 1
name: kafka-lab

services:                    # infra services, from the catalog
  kafka:
    type: kafka
  postgres:
    type: postgres
    resources:               # optional; overrides catalog defaults
      memory: 512m
      cpus: 0.5
    config:
      database: orders

apps:                        # user code; one compose service each
  producer:
    path: ./orders           # directory containing the Dockerfile
    command: [go, run, ./cmd/producer]
    uses: [kafka]
  consumer:
    path: ./orders
    command: [go, run, ./cmd/consumer]
    uses:
      - kafka
      - service: postgres
        tls:
          mode: off
          reason: practice TLS termination at a pooler
    ports:                   # optional; published on 127.0.0.1 only
      - 8080
    health:
      path: /ready
      port: 9000
`

func load(t *testing.T, src string) (*Spec, error) {
	t.Helper()

	doc, err := Parse([]byte(src))
	if err != nil {
		return nil, err
	}

	s, err := doc.Decode()
	if err != nil {
		return nil, err
	}

	return s, Validate(s, types)
}

func paths(t *testing.T, err error) []string {
	t.Helper()

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}

	if !errors.Is(err, errs.ErrValidation) {
		t.Fatal("ValidationError does not match ErrValidation")
	}

	var out []string
	for _, v := range ve.Violations {
		out = append(out, v.Path)
	}

	slices.Sort(out)

	return out
}

func TestExampleDecodes(t *testing.T) {
	s, err := load(t, example)
	if err != nil {
		t.Fatal(err)
	}

	if s.Name != "kafka-lab" || len(s.Services) != 2 || len(s.Apps) != 2 {
		t.Fatalf("spec = %+v", s)
	}

	if s.Services[0].Name != "kafka" || s.Services[1].Name != "postgres" {
		t.Errorf("service order = %v", s.Services)
	}

	pg := s.Services[1]
	if pg.Resources != (Resources{Memory: "512m", CPUs: "0.5"}) || pg.Config["database"] != "orders" {
		t.Errorf("postgres = %+v", pg)
	}

	c := s.Apps[1]
	if len(c.Uses) != 2 || c.Uses[0] != (Use{Service: "kafka"}) {
		t.Fatalf("uses = %+v", c.Uses)
	}

	if c.Uses[1].Service != "postgres" || !c.Uses[1].TLS.Plaintext() || c.Uses[1].TLS.Reason == "" {
		t.Errorf("long-form use = %+v", c.Uses[1])
	}

	if !slices.Equal(c.Ports, []int{8080}) || c.Health == nil || *c.Health != (Health{Path: "/ready", Port: 9000}) {
		t.Errorf("ports/health = %v %+v", c.Ports, c.Health)
	}
}

func TestDecodeReportsEveryStructuralProblem(t *testing.T) {
	src := `version: 1
name: x
colour: blue
services:
  kafka:
    type: kafka
    replicas: 3
    upstream: [postgres]
  bare:
apps:
  web:
    command: go run .
    uses:
      - service: kafka
        tls:
          mode: maybe
          clientAuth: require
          ca: lab
    ports: [http]
`

	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	_, err = doc.Decode()

	want := []string{
		"apps.web.command",
		"apps.web.path",
		"apps.web.ports[0]",
		"apps.web.uses[0].tls.ca",
		"apps.web.uses[0].tls.clientAuth",
		"apps.web.uses[0].tls.mode",
		"colour",
		"services.bare.type",
		"services.kafka.replicas",
		"services.kafka.upstream",
	}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("violation paths:\n got %v\nwant %v", got, want)
	}

	msg := err.Error()
	for _, s := range []string{"reserved for Phase 6 (mTLS)", "reserved for Phase 6 (multiple CAs)", "services.kafka.upstream: unknown key", "unknown key", "must be on or off"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error message lacks %q:\n%s", s, msg)
		}
	}
}

func TestValidateReportsEveryRule(t *testing.T) {
	src := `version: 1
name: Bad_Name
services:
  kafka:
    type: kafka
  db:
    type: postgres
    resources: {memory: lots, cpus: "-1"}
    config: {database: "", color: red}
  cache:
    type: redis
  queue:
    type: rabbitmq
apps:
  kafka:
    path: ./dup
  web:
    path: /abs/path
    command: [""]
    uses:
      - missing
      - service: db
        tls: {mode: off}
      - service: cache
        tls: {mode: off, reason: measure overhead}
      - kafka
      - kafka
    ports: [0, 8080, 8080]
    health: {path: healthz, port: 70000}
`
	_, err := load(t, src)

	want := []string{
		"apps.kafka",
		"apps.web.command[0]",
		"apps.web.health.path",
		"apps.web.health.port",
		"apps.web.path",
		"apps.web.ports[0]",
		"apps.web.ports[2]",
		"apps.web.uses[0]",
		"apps.web.uses[1].tls.reason",
		"apps.web.uses[2].tls.mode",
		"apps.web.uses[4]",
		"name",
		"services.db.config.color",
		"services.db.config.database",
		"services.db.resources.cpus",
		"services.db.resources.memory",
		"services.queue.type",
	}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("violation paths:\n got %v\nwant %v", got, want)
	}

	if !strings.Contains(err.Error(), "tls.mode is off") {
		t.Errorf("missing reason message: %v", err)
	}
}

func TestNewerVersionIsRefused(t *testing.T) {
	doc, err := Parse([]byte("version: 2\nname: x\n"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := doc.Decode(); !errors.Is(err, errs.ErrNewerFormat) {
		t.Fatalf("Decode error = %v, want ErrNewerFormat", err)
	}

	if _, _, err := doc.Migrate(); !errors.Is(err, errs.ErrNewerFormat) {
		t.Fatalf("Migrate error = %v, want ErrNewerFormat", err)
	}
}

func TestMigrateAtCurrentVersionIsNoop(t *testing.T) {
	doc, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}

	from, changed, err := doc.Migrate()
	if err != nil || changed || from != CurrentVersion {
		t.Fatalf("Migrate = %d %v %v", from, changed, err)
	}
}

func TestMigrateRunsStepsInOrder(t *testing.T) {
	// Simulate a future where the current version is reached through a step.
	upgrades[0] = func(*Document) error { return nil }

	t.Cleanup(func() { delete(upgrades, 0) })

	doc, err := Parse([]byte("version: 0\nname: x\n"))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := doc.Migrate(); err == nil {
		t.Fatal("version 0 must be rejected: versions start at 1")
	}
}

func TestEditsPreserveComments(t *testing.T) {
	doc, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}

	if err := doc.AddService("cache", "redis"); err != nil {
		t.Fatal(err)
	}

	if err := doc.AddService("kafka", "kafka"); !errors.Is(err, errs.ErrExists) {
		t.Fatalf("duplicate AddService error = %v", err)
	}

	if err := doc.AddApp(App{Name: "worker", Path: "./worker", Command: []string{"go", "run", "."}, Uses: []Use{{Service: "cache"}}}); err != nil {
		t.Fatal(err)
	}

	if section, err := doc.Remove("producer"); err != nil || section != "apps" {
		t.Fatalf("Remove = %q %v", section, err)
	}

	if _, err := doc.Remove("nope"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Remove missing error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	text := string(out)
	for _, c := range []string{"# infra services, from the catalog", "# optional; overrides catalog defaults", "# user code; one compose service each", "# optional; published on 127.0.0.1 only"} {
		if !strings.Contains(text, c) {
			t.Errorf("comment %q lost:\n%s", c, text)
		}
	}

	reparsed, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}

	s, err := reparsed.Decode()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Service("cache"); !ok {
		t.Error("cache missing after round trip")
	}

	if _, ok := s.App("producer"); ok {
		t.Error("producer still present")
	}

	if w, ok := s.App("worker"); !ok || w.Uses[0].Service != "cache" || !slices.Equal(w.Command, []string{"go", "run", "."}) {
		t.Errorf("worker = %+v", w)
	}

	if err := Validate(s, types); err != nil {
		t.Errorf("edited spec is invalid: %v", err)
	}
}

func TestNewDocument(t *testing.T) {
	doc := New("lab")
	if err := doc.AddService("kafka", "kafka"); err != nil {
		t.Fatal(err)
	}

	out, _ := doc.Bytes()

	s, err := load(t, string(out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	if s.Version != CurrentVersion || s.Name != "lab" || len(s.Services) != 1 {
		t.Fatalf("spec = %+v", s)
	}

	if !strings.Contains(string(out), "bb add <type>") {
		t.Errorf("guidance comment missing:\n%s", out)
	}
}

func TestParseRejectsNonMapping(t *testing.T) {
	for _, src := range []string{"", "- a\n- b\n", "name: [unclosed\n"} {
		if _, err := Parse([]byte(src)); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("Parse(%q) error = %v, want ErrValidation", src, err)
		}
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	src := `version: 1
name: x
services:
  a:
    type: ""
  db:
    type: postgres
    resources: {memory: "0", cpus: Inf}
  db2:
    type: postgres
    resources: {cpus: "0x1p-2"}
apps:
  web:
    path: "./src:/etc"
  gen:
    path: .bb/runtime
`
	_, err := load(t, src)

	want := []string{
		"apps.gen.path",
		"apps.web.path",
		"services.a.type",
		"services.db.resources.cpus",
		"services.db.resources.memory",
		"services.db2.resources.cpus",
	}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("violation paths:\n got %v\nwant %v", got, want)
	}
}

func TestNamesThatLookLikeOtherTypesStayStrings(t *testing.T) {
	for _, name := range []string{"null", "true", "123", "on"} {
		doc := New(name)
		out, _ := doc.Bytes()

		s, err := load(t, string(out))
		if err != nil || s.Name != name {
			t.Errorf("New(%q) round trip = %+v %v\n%s", name, s, err, out)
		}
	}
}

func TestRegister(t *testing.T) {
	ok := `version: 1
name: lab
services:
  consul:
    type: consul
apps:
  greeter:
    path: ./g
    uses: [consul]
    register:
      port: 8443
      check: /healthz
      tags: [traefik.enable=true, v1]
`

	s, err := load(t, ok)
	if err != nil {
		t.Fatal(err)
	}

	if r := s.Apps[0].Register; r == nil || r.Port != 8443 || r.Check != "/healthz" || !slices.Equal(r.Tags, []string{"traefik.enable=true", "v1"}) {
		t.Fatalf("register = %+v", r)
	}

	missing, err := Parse([]byte("version: 1\nname: lab\napps:\n  g:\n    path: ./g\n    register: {check: /x}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := missing.Decode(); !slices.Equal(paths(t, err), []string{"apps.g.register.port"}) {
		t.Fatalf("decode violations = %v", paths(t, err))
	}

	notList, err := Parse([]byte("version: 1\nname: lab\napps:\n  g:\n    path: ./g\n    register: {port: 8443, tags: a}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := notList.Decode(); err == nil || !strings.Contains(err.Error(), "apps.g.register.tags: must be a list of strings") {
		t.Fatalf("tags that are not a list = %v", err)
	}

	bad := `version: 1
name: lab
services:
  consul:
    type: consul
  cache:
    type: redis
apps:
  badfields:
    path: ./a
    uses: [consul]
    register: {port: 8443, name: Bad_Name, check: healthz}
  noregistry:
    path: ./b
    uses: [cache]
    register: {port: 70000}
  web:
    path: ./c
    uses: [consul]
    register: {port: 443}
  web-agent:
    path: ./d
  badtags:
    path: ./e
    uses: [consul]
    register: {port: 8443, tags: [a, "", " b", "c ", a]}
`
	_, err = load(t, bad)

	want := []string{
		"apps.badfields.register.check",
		"apps.badfields.register.name",
		"apps.badtags.register.tags[1]",
		"apps.badtags.register.tags[2]",
		"apps.badtags.register.tags[3]",
		"apps.badtags.register.tags[4]",
		"apps.noregistry.register",
		"apps.noregistry.register.port",
		"apps.web-agent",
	}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("violation paths:\n got %v\nwant %v", got, want)
	}

	for _, msg := range []string{
		"apps.badtags.register.tags[1]: must not be empty",
		"apps.badtags.register.tags[2]: must not start or end with whitespace",
		"apps.badtags.register.tags[3]: must not start or end with whitespace",
		"apps.badtags.register.tags[4]: a is listed more than once",
	} {
		if !strings.Contains(err.Error(), msg) {
			t.Errorf("violations lack %q:\n%v", msg, err)
		}
	}

	// AddApp writes register so it round-trips.
	d := New("lab")
	if err := d.AddService("consul", "consul"); err != nil {
		t.Fatal(err)
	}

	tags := []string{"traefik.enable=true", "traefik.http.routers.greeter.rule=PathPrefix(`/`)"}
	if err := d.AddApp(App{Name: "greeter", Path: "./g", Uses: []Use{{Service: "consul"}}, Register: &Register{Port: 8443, Tags: tags}}); err != nil {
		t.Fatal(err)
	}

	out, _ := d.Bytes()

	s, err = load(t, string(out))
	if err != nil || s.Apps[0].Register == nil || s.Apps[0].Register.Port != 8443 || !slices.Equal(s.Apps[0].Register.Tags, tags) {
		t.Fatalf("round trip = %+v %v\n%s", s, err, out)
	}
}
