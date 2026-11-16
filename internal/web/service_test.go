package web

import (
	"context"
	"net/http"
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
	t.Cleanup(func() { s.ln.Close() })
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
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `data-load="/fragments/explore/postgres"`) || !strings.Contains(body, ">Tables<") {
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
	if body := s.get(t, "/", cookie).Body.String(); !strings.Contains(body, "Overview") || strings.Contains(body, "What do you want to build?") {
		t.Error("with an experiment, / is not the dashboard")
	}
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
	sink := s.get(t, "/starters/kafka/sink-to-postgres", cookie).Body.String()
	for _, want := range []string{"github.com/jackc/pgx/v5", "uses: [kafka, postgres]", "ON CONFLICT (id) DO NOTHING"} {
		if !strings.Contains(sink, want) {
			t.Errorf("sink-to-postgres page lacks %q", want)
		}
	}
	if sd := s.get(t, "/starters/consul/service-discovery", cookie).Body.String(); !strings.Contains(sd, "register: {port: 8443}") {
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
