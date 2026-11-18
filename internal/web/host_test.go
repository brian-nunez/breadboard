package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/core"
	"github.com/brian-nunez/breadboard/internal/spec"
)

const hostSpec = `version: 1
name: shop
services:
  edge:
    type: haproxy
    upstreams:
      - service: api
        port: 8443
    host: true
apps:
  api:
    path: ./api
    command: [api]
    replicas: 3
    host:
      port: 8443
`

// newHostProject is a server over an experiment with host targets whose
// host port is recorded, as after its first bb up.
func newHostProject(t *testing.T, port int) *Server {
	t.Helper()
	dir := t.TempDir()

	fixture, err := os.ReadFile(filepath.Join("..", "core", "testdata", "explore", "traefik-api.txt"))
	if err != nil {
		t.Fatal(err)
	}

	rt := fixtureRuntime{out: map[string]string{"bb-host": string(fixture)}}

	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: rt, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Init(context.Background(), core.InitRequest{Dir: dir, Name: "shop"}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(hostSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	if port != 0 {
		path := filepath.Join(dir, ".bb", "state.json")

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var st map[string]any
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatal(err)
		}

		st["hostPort"] = port
		if data, err = json.Marshal(st); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s, err := New(eng, Options{Dir: dir, Refresh: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.ln.Close() })

	return s
}

// Host URLs show as links on each target's page, on the overview and the
// architecture inspector; bb-host's page says what it is and lists its
// routes, and its Traefik explorer still works.
func TestHostAccessPages(t *testing.T) {
	s := newHostProject(t, 44312)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	link := func(u string) string { return `href="` + u + `" target="_blank" rel="noopener"` }

	const (
		edgeURL = "https://edge.shop.localhost:44312"
		apiURL  = "https://api.shop.localhost:44312"
		api2URL = "https://api-2.shop.localhost:44312"
	)
	for path, wants := range map[string][]string{
		"/dashboard": {
			"Host access", "127.0.0.1:44312", link(edgeURL), link(apiURL), link(api2URL),
			"every replica of api", "api-2 only, one replica of api", `href="/services/bb-host"`, "host router · traefik:",
		},
		"/services/edge":                 {"From the host", link(edgeURL)},
		"/services/api-2":                {"From the host", link(apiURL), link(api2URL)},
		"/services/api-2?tab=connection": {"From the host", link(api2URL)},
		"/services/bb-host": {
			"host router. It is the only container published", "127.0.0.1:44312", "Host routes", link(edgeURL), link(api2URL),
			"every replica of api", `href="/services/api-1"`, `href="/services/api-2"`, `href="/docs/getting-started/host-access"`, "Traefik routes", "?tab=explore",
		},
		"/architecture?node=bb-host":             {">host router</text>", "Routes from the host", link(apiURL)},
		"/architecture?node=api-1":               {"From the host", link(apiURL)},
		"/fragments/explore/bb-host?tab=explore": {"web@file"},
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

		if strings.Contains(body, "<script>") || strings.Contains(body, " style=") {
			t.Errorf("%s has an inline script or style", path)
		}
	}

	if body := s.get(t, "/services/api-1", cookie).Body.String(); strings.Contains(body, api2URL) {
		t.Error("api-1's page shows api-2's own URL")
	}
	// The router is not a proxy a learner configured: no try/break notes.
	if body := s.get(t, "/services/bb-host", cookie).Body.String(); strings.Contains(body, "Break this") {
		t.Error("bb-host's page shows Traefik's exercises")
	}
}

// Before the first bb up no port is recorded: no URLs anywhere, and the
// router's page says when they appear.
func TestHostAccessBeforeFirstUp(t *testing.T) {
	s := newHostProject(t, 0)

	cookie := map[string]string{"Cookie": signIn(t, s)}
	if body := s.get(t, "/dashboard", cookie).Body.String(); strings.Contains(body, ".localhost") || strings.Contains(body, ">Host access<") {
		t.Error("overview shows host access before a port is recorded")
	}

	body := s.get(t, "/services/bb-host", cookie).Body.String()
	for _, w := range []string{"The URLs appear after the next", "a port chosen at the next bb up"} {
		if !strings.Contains(body, w) {
			t.Errorf("bb-host lacks %q", w)
		}
	}
}

// The host access guide renders and is in search.
func TestHostAccessDocs(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	const path = "/docs/getting-started/host-access"

	rec := s.get(t, path, cookie)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d", path, rec.Code)
	}

	for _, w := range []string{">Host access</h1>", "bb ca --host --pem", "curl --resolve", "X-Forwarded-Host", "44300 to 44999", "host: true"} {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q", path, w)
		}
	}

	var entries []searchEntry
	if err := json.Unmarshal(s.get(t, "/search-index", cookie).Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range entries {
		found = found || (e.URL == path && e.Title == "Host access")
	}

	if !found {
		t.Errorf("search has no %s", path)
	}
}

// The guide's experiment.yaml is valid against the real catalog.
func TestHostAccessDocsExampleIsValid(t *testing.T) {
	doc, err := spec.Parse([]byte(hostAccessYAML))
	if err != nil {
		t.Fatal(err)
	}

	sp, err := doc.Decode()
	if err != nil {
		t.Fatal(err)
	}

	if err := spec.Validate(sp, catalog.Types); err != nil {
		t.Fatal(err)
	}

	if len(sp.Apps) != 1 || sp.Apps[0].Host == nil || len(sp.Services) != 1 || sp.Services[0].Host == nil {
		t.Error("the example has no host targets")
	}
}
