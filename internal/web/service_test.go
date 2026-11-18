package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/core"
)

func TestParseNotes(t *testing.T) {
	n := parseNotes("# Kafka\n\n## What you got\n\nOne broker at `kafka:9093`,\nwith **TLS**.\n\n## Try this\n\n- Run `bb up`.\n- Watch <script>x</script>.\n")
	if n.Title != "Kafka" || len(n.Sections) != 2 {
		t.Fatalf("notes = %+v", n)
	}

	p := n.section("what you got").Blocks
	if len(p) != 1 || p[0].List {
		t.Fatalf("paragraph = %+v", p)
	}

	want := []inline{{Text: "One broker at "}, {Text: "kafka:9093", Code: true}, {Text: ", with "}, {Text: "TLS", Bold: true}, {Text: "."}}
	if !slices.Equal(p[0].Items[0], want) {
		t.Errorf("inlines = %+v", p[0].Items[0])
	}

	if b := n.section("Try this").bullets(); len(b) != 2 || b[0][1] != (inline{Text: "bb up", Code: true}) {
		t.Errorf("bullets = %+v", b)
	}

	if got := inlines("a * b and `unclosed"); len(got) == 0 {
		t.Error("stray markers dropped the text")
	}
}

// newProject is a server over an initialized experiment with postgres and an
// app that uses it, on a Docker that runs nothing.
func newProject(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()

	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: nullRuntime{}, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := eng.Init(ctx, core.InitRequest{Dir: dir, Name: "lab"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, core.ApplyStarterRequest{Dir: dir, ID: "kafka/sink-to-postgres"}); err != nil {
		t.Fatal(err)
	}

	s, err := New(eng, Options{Dir: dir, Refresh: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.ln.Close() })

	return s
}

func TestServicePages(t *testing.T) {
	s := newProject(t)

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for path, wants := range map[string][]string{
		"/services/postgres":                {"What you're running", "Env vars injected into apps", "DATABASE_URL", "hidden", "bb creds postgres", "Issued on the first", "Try this", "Break this"},
		"/services/postgres?tab=connection": {"What apps receive", "bb creds postgres"},
		"/services/postgres?tab=learn":      {"Read the Postgres docs", "/docs/postgres"},
		"/services/sink":                    {"your code in ./sink-to-postgres", "It connects to kafka, postgres"},
		"/services/sink?tab=connection":     {"Environment of sink", "SSL_CERT_FILE", "BB_FQDN", "postgres · Postgres"},
		"/services/sink?tab=logs":           {`data-logs="/live/logs/sink"`},
		"/services/sink?tab=learn":          {"What you're running"}, // apps have no Learn tab: falls back
	} {
		rec := s.get(t, path, cookie)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", path, rec.Code)

			continue
		}

		for _, w := range wants {
			if !strings.Contains(rec.Body.String(), w) {
				t.Errorf("%s lacks %q", path, w)
			}
		}
	}

	if rec := s.get(t, "/services/nope", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("unknown service = %d", rec.Code)
	}
}

// An explorer on a service that is not running explains itself instead of
// failing the page.
func TestExplorerTabWhenTheServiceIsDown(t *testing.T) {
	s := newProject(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	rec := s.get(t, "/services/postgres?tab=explore", cookie)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `data-load="/fragments/explore/postgres?tab=explore"`) || !strings.Contains(body, ">Tables<") {
		t.Errorf("explore tab = %d\n%s", rec.Code, body)
	}

	rec = s.get(t, "/fragments/explore/postgres", cookie)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "Could not read postgres") || strings.Contains(body, "<html") {
		t.Errorf("explore fragment = %d\n%s", rec.Code, body)
	}

	if rec := s.get(t, "/fragments/explore/sink", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("explorer for an app = %d", rec.Code)
	}
}

func TestStarterPages(t *testing.T) {
	s := newProject(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	list := s.get(t, "/starters", cookie).Body.String()
	for id := range starterContents {
		if !strings.Contains(list, `href="/starters/`+id+`"`) {
			t.Errorf("starter list lacks %s", id)
		}

		rec := s.get(t, "/starters/"+id, cookie)

		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("/starters/%s = %d", id, rec.Code)

			continue
		}

		for _, want := range []string{"About this example", "How it works", "Files it creates", "Added to experiment.yaml", "bb starter " + id, "Libraries", ">Try<", ">Break<"} {
			if !strings.Contains(body, want) {
				t.Errorf("/starters/%s lacks %q", id, want)
			}
		}
	}

	sink := plainText([]string{s.get(t, "/starters/kafka/sink-to-postgres", cookie).Body.String()})
	for _, want := range []string{"github.com/jackc/pgx/v5", "uses: [kafka, postgres]", "ON CONFLICT (id) DO NOTHING"} {
		if !strings.Contains(sink, want) {
			t.Errorf("sink-to-postgres page lacks %q", want)
		}
	}

	if sd := plainText([]string{s.get(t, "/starters/consul/service-discovery", cookie).Body.String()}); !strings.Contains(sd, "register: {port: 8443}") {
		t.Error("service-discovery page lacks the register line")
	}

	if rec := s.get(t, "/starters/kafka/nope", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("unknown starter = %d", rec.Code)
	}
}

func TestDirectRequires(t *testing.T) {
	got := directRequires("module x\n\nrequire github.com/a/b v1.0.0\n\nrequire (\n\tgithub.com/c/d v1.0.0\n\tgithub.com/e/f v1.0.0 // indirect\n)\n")
	if !slices.Equal(got, []string{"github.com/a/b", "github.com/c/d"}) {
		t.Errorf("directRequires = %v", got)
	}
}

func TestSearchIndexListsEveryPage(t *testing.T) {
	s := newProject(t)
	rec := s.get(t, "/search-index", map[string]string{"Cookie": signIn(t, s)})

	var entries []searchEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("index: %v\n%s", err, rec.Body.String())
	}

	urls := map[string]bool{}
	for _, e := range entries {
		urls[e.URL] = true
	}

	for _, want := range []string{"/docs/kafka", "/docs/kafka/consumer-groups", "/starters/kafka/sink-to-postgres", "/services/sink", "/services/postgres"} {
		if !urls[want] {
			t.Errorf("index lacks %s", want)
		}
	}
}

// Phones get a menu and container cards; wider screens the table.
func TestMobileLayout(t *testing.T) {
	s := newProject(t)

	body := s.get(t, "/dashboard", map[string]string{"Cookie": signIn(t, s)}).Body.String()
	for _, want := range []string{"<details", ">Menu</summary>", "md:hidden", "hidden md:block", "data-search-open", `id="search"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
}

// fixtureRuntime answers exec with captured explorer output, by service.
type fixtureRuntime struct {
	nullRuntime
	out map[string]string
}

func (f fixtureRuntime) Exec(_ context.Context, _, service string, _ []string) ([]byte, error) {
	if s, ok := f.out[service]; ok {
		return []byte(s), nil
	}

	return nil, errors.New("not running")
}

const sidecarSpec = `version: 1
name: lab
services:
  postgres:
    type: postgres
apps:
  web:
    path: ./web
    command: [web]
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  caller:
    path: ./caller
    command: [caller]
    uses: [postgres]
    sidecars:
      - type: envoy
        outbound: [{service: web, port: 9001}]
        transparent: {egress: deny}
      - type: pgbouncer
        service: postgres
`

// Sidecars, replicas and the sidecar explorers render, and no generated
// secret appears on any page.
func TestSidecarAndReplicaPages(t *testing.T) {
	fixture := func(name string) string {
		data, err := os.ReadFile(filepath.Join("..", "core", "testdata", "explore", name))
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}
	dir := t.TempDir()
	rt := fixtureRuntime{out: map[string]string{"caller-envoy": fixture("envoy-outbound.txt"), "caller-pgbouncer": fixture("pgbouncer-show.txt")}}

	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: rt, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := eng.Init(ctx, core.InitRequest{Dir: dir, Name: "lab"}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(sidecarSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	creds, err := eng.Creds(ctx, core.CredsRequest{Dir: dir}) // generates the secrets
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(eng, Options{Dir: dir, Refresh: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.ln.Close() })

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for path, wants := range map[string][]string{
		"/architecture?node=caller":               {"envoy · transparent", "×3", "TLS :8443", `href="/architecture?node=web-1-envoy"`, "Sidecars", "caller-pgbouncer"},
		"/architecture?node=caller-envoy":         {"Listens on", "127.0.0.1:15000", ":15001"},
		"/services/caller":                        {"caller-envoy", "Envoy sidecar", "WEB_URL", "http://127.0.0.1:9001", "Transparent mode", "refused (egress deny)", "?tab=envoy", "?tab=pgbouncer", "DATABASE_URL", "bb creds postgres", "PGHOST"},
		"/services/web-2":                         {"Replicas", "web-3", "replica 2 of 3", "BB_LISTEN_ADDR", "127.0.0.1:8080"},
		"/services/web-2-envoy":                   {"Envoy sidecar of web-2", "s pod: same network namespace", "?tab=envoy"},
		"/fragments/explore/caller?tab=envoy":     {"outbound_web → web", "web-3", "Healthy, with errors", "172.21.0.5:8443", "bb logs caller-envoy -f"},
		"/fragments/explore/caller?tab=pgbouncer": {"caller-pgbouncer → postgres", "app / app", "transaction", "-p 6432"},
	} {
		rec := s.get(t, path, cookie)

		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", path, rec.Code)

			continue
		}

		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", path, w)
			}
		}

		for _, c := range creds.Services {
			for _, sec := range c.Secrets {
				if len(sec.Value) >= 8 && strings.Contains(body, sec.Value) {
					t.Errorf("%s shows the secret %s of %s", path, sec.Key, c.Name)
				}
			}
		}
	}

	if rec := s.get(t, "/fragments/explore/postgres?tab=envoy", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("envoy tab on postgres = %d", rec.Code)
	}

	if rec := s.get(t, "/services/web-2-envoy?tab=learn", cookie); strings.Contains(rec.Body.String(), "?tab=learn") {
		t.Error("a sidecar without notes has a Learn tab")
	}
}
