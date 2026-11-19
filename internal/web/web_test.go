package web

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/core"
	"github.com/brian-nunez/breadboard/internal/runtime"
)

// nullRuntime is a Docker that is available and runs nothing.
type nullRuntime struct{}

func (nullRuntime) Available(context.Context) error                        { return nil }
func (nullRuntime) Up(context.Context, runtime.Project) error              { return nil }
func (nullRuntime) Down(context.Context, runtime.Project, bool) error      { return nil }
func (nullRuntime) RemoveOwned(context.Context, string, bool) error        { return nil }
func (nullRuntime) Subnets(context.Context) ([]runtime.Network, error)     { return nil, nil }
func (nullRuntime) Build(context.Context, runtime.Project, []string) error { return nil }
func (nullRuntime) ImageUser(context.Context, runtime.Project, string) (runtime.ImageUser, error) {
	return runtime.ImageUser{}, nil
}
func (nullRuntime) Owners(context.Context, string) ([]string, error) { return nil, nil }
func (nullRuntime) PublishedPorts(context.Context) ([]runtime.PublishedPort, error) {
	return nil, nil
}

func (nullRuntime) Exec(context.Context, string, string, []string) ([]byte, error) {
	return nil, errors.New("not running")
}

func (nullRuntime) Containers(context.Context, string) ([]runtime.Container, error) {
	return nil, nil
}

func (nullRuntime) Stats(context.Context, []string) (map[string]runtime.Usage, error) {
	return map[string]runtime.Usage{}, nil
}

func (nullRuntime) StatsStream(context.Context) iter.Seq2[runtime.Sample, error] {
	return func(func(runtime.Sample, error) bool) {}
}

func (nullRuntime) Logs(context.Context, runtime.Project, []string, bool, int) iter.Seq2[runtime.LogLine, error] {
	return func(func(runtime.LogLine, error) bool) {}
}

func newServer(t *testing.T) *Server {
	t.Helper()

	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: nullRuntime{}, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(eng, Options{Dir: t.TempDir(), Refresh: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.ln.Close() })

	return s
}

func (s *Server) get(t *testing.T, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:" + s.port

	for k, v := range header {
		if k == "Host" {
			req.Host = v

			continue
		}

		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	return rec
}

// signIn follows the printed URL and returns the session cookie header.
func signIn(t *testing.T, s *Server) string {
	t.Helper()

	rec := s.get(t, "/auth?token="+s.token, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("auth = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	c := rec.Result().Cookies()
	if len(c) != 1 || c[0].Name != sessionCookie || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %+v", c)
	}

	return c[0].Name + "=" + c[0].Value
}

func TestEveryPageNeedsTheLaunchLink(t *testing.T) {
	s := newServer(t)
	for _, path := range []string{"/", "/live/overview", "/auth?token=wrong", "/auth"} {
		rec := s.get(t, path, nil)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Open the link that bb serve printed") {
			t.Errorf("%s without a session = %d", path, rec.Code)
		}
	}

	if rec := s.get(t, "/static/app.css", nil); rec.Code != http.StatusOK {
		t.Errorf("stylesheet for the sign-in page = %d", rec.Code)
	}

	cookie := signIn(t, s)
	if rec := s.get(t, "/", map[string]string{"Cookie": cookie}); rec.Code != http.StatusOK {
		t.Fatalf("signed-in overview = %d", rec.Code)
	}

	if rec := s.get(t, "/", map[string]string{"Cookie": sessionCookie + "=forged"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("forged session = %d", rec.Code)
	}
}

// DNS rebinding and cross-site requests arrive with someone else's Host or
// Origin; both are refused even with a valid session.
func TestForeignHostsAndOriginsAreRefused(t *testing.T) {
	s := newServer(t)
	cookie := signIn(t, s)

	cases := []struct {
		header map[string]string
		want   int
	}{
		{map[string]string{"Host": "evil.example:" + s.port}, http.StatusMisdirectedRequest},
		{map[string]string{"Host": "127.0.0.1:1"}, http.StatusMisdirectedRequest},
		{map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{map[string]string{"Origin": "http://127.0.0.1:" + s.port}, http.StatusOK},
		{map[string]string{"Host": "localhost:" + s.port}, http.StatusOK},
	}
	for _, c := range cases {
		c.header["Cookie"] = cookie
		if rec := s.get(t, "/", c.header); rec.Code != c.want {
			t.Errorf("%v = %d, want %d", c.header, rec.Code, c.want)
		}
	}

	rec := s.get(t, "/", map[string]string{"Cookie": cookie})
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestAuthRedirectsOnlyWithinTheSite(t *testing.T) {
	s := newServer(t)
	for next, want := range map[string]string{"/docs": "/docs", "//evil.example": "/", "https://evil.example": "/", "docs": "/"} {
		rec := s.get(t, "/auth?token="+s.token+"&next="+next, nil)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("next=%q redirects to %q, want %q", next, got, want)
		}
	}
}

func TestOnlyLoopbackAddresses(t *testing.T) {
	eng, err := core.New(core.Options{Home: t.TempDir(), Runtime: nullRuntime{}})
	if err != nil {
		t.Fatal(err)
	}

	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.10:0"} {
		if s, err := New(eng, Options{Addr: addr}); err == nil {
			_ = s.ln.Close()

			t.Errorf("New(%q) listened", addr)
		}
	}
}

func TestNoExperimentShowsHowToStart(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	rec := s.get(t, "/dashboard", cookie)

	body := rec.Body.String()
	for _, want := range []string{"No experiment here yet", "bb init kafka-lab", "data-copy"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty overview lacks %q", want)
		}
	}
}

// / and /home are always the landing page of frame 01, with or without an
// experiment; the dashboard overview is at /dashboard.
func TestLandingPage(t *testing.T) {
	for name, s := range map[string]*Server{"no experiment": newServer(t), "experiment": newProject(t)} {
		cookie := map[string]string{"Cookie": signIn(t, s)}
		for _, path := range []string{"/", "/home"} {
			rec := s.get(t, path, cookie)

			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Errorf("%s %s = %d", name, path, rec.Code)

				continue
			}

			for _, want := range []string{"What do you want to build?", "Pick a scenario to see its architecture and how to run it.", `href="/docs/getting-started"`, "New here? Start with Getting started →", `href="/dashboard"`, "Skip to dashboard →", "data-search-shortcut", "data-search-open", "Browse all starters", "Open catalog →"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s %s lacks %q", name, path, want)
				}
			}

			if strings.Contains(body, "Read-only") || strings.Contains(body, `data-live="/live/overview"`) {
				t.Errorf("%s %s is not only the landing page", name, path)
			}

			last := -1

			for _, id := range suggestedStarters {
				i := strings.Index(body, `href="/starters/`+id+`"`)
				if i < last {
					t.Errorf("%s %s: %s missing or out of order", name, path, id)
				}

				last = i
			}

			if n := strings.Count(body, `href="/starters/`); n != len(suggestedStarters) {
				t.Errorf("%s %s links %d starters, want %d", name, path, n, len(suggestedStarters))
			}
			// lb/consul-traefik took consul/service-discovery's card (D-227).
			if !strings.Contains(body, `href="/starters/lb/consul-traefik"`) || strings.Contains(body, `href="/starters/consul/service-discovery"`) {
				t.Errorf("%s %s: the fourth card is not lb/consul-traefik", name, path)
			}

			if i := strings.Index(body, "Browse all starters"); i < last {
				t.Errorf("%s %s: browse card is not last", name, path)
			}
		}

		dash := s.get(t, "/dashboard", cookie).Body.String()
		if !strings.Contains(dash, "Overview") || strings.Contains(dash, "What do you want to build?") {
			t.Errorf("%s /dashboard is not the overview", name)
		}
	}
}

// The landing page shows each suggestion's registry title, summary and tech.
func TestLandingSuggestionsComeFromTheRegistry(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	body := s.get(t, "/", cookie).Body.String()

	d := s.docsData(httptest.NewRequest(http.MethodGet, "/", nil))
	for _, id := range suggestedStarters {
		st, ok := d.Starters[id]
		if !ok {
			t.Fatalf("starter %s is not in the registry", id)
		}

		want := []string{cardTitle(st.Title), templEscape(st.Summary)}
		for _, typ := range st.Requires {
			want = append(want, techName(typ, d.Catalog))
		}

		for _, want := range want {
			if !strings.Contains(body, want) {
				t.Errorf("card %s lacks %q", id, want)
			}
		}
	}
}

// fromIndex returns s from the first sub on, failing the test if s has no sub.
func fromIndex(t *testing.T, s, sub string) string {
	t.Helper()

	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("%q not found in:\n%s", sub, s)
	}

	return s[i:]
}

// untilIndex returns s up to the first sub, failing the test if s has no sub.
func untilIndex(t *testing.T, s, sub string) string {
	t.Helper()

	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("%q not found in:\n%s", sub, s)
	}

	return s[:i]
}

func templEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "'", "&#39;", "\"", "&#34;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Getting started is the sidebar's first link; no page says Read-only.
func TestSidebarStartsWithGettingStarted(t *testing.T) {
	s := newProject(t)

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for _, path := range []string{"/dashboard", "/architecture", "/services/postgres"} {
		body := s.get(t, path, cookie).Body.String()
		aside := fromIndex(t, body, "<aside")
		first := fromIndex(t, aside, "<a ")

		first = untilIndex(t, first, "</a>")
		if !strings.Contains(first, `href="/docs/getting-started"`) || !strings.Contains(first, "text-indigo-700") || !strings.HasSuffix(first, ">Getting started") {
			t.Errorf("%s sidebar's first link = %s", path, first)
		}

		if n := strings.Count(untilIndex(t, aside, "</aside>"), `href="/docs/getting-started"`); n != 1 {
			t.Errorf("%s sidebar links Getting started %d times", path, n)
		}
	}

	for _, path := range []string{"/", "/home", "/dashboard", "/architecture", "/services/postgres", "/docs", "/docs/getting-started", "/starters", "/starters/kafka/producer-consumer"} {
		if body := s.get(t, path, cookie).Body.String(); strings.Contains(body, "Read-only") {
			t.Errorf("%s says Read-only", path)
		}
	}
}

func TestLiveOverviewStreamsUpdates(t *testing.T) {
	s := newServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/live/overview", nil)
	req.Host = "127.0.0.1:" + s.port
	req.Header.Set("Cookie", signIn(t, s))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	// The page is unchanged between ticks, so it is sent once.
	if n := strings.Count(rec.Body.String(), "event: update\n"); n != 1 {
		t.Errorf("updates = %d, want 1:\n%s", n, rec.Body.String())
	}
}

// The search hint defaults to Ctrl K, the key most platforms use; app.js
// switches it to ⌘K on Apple platforms, which the static script must do.
func TestSearchShortcutHint(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	body := s.get(t, "/docs", cookie).Body.String()
	if !strings.Contains(body, `<kbd data-search-shortcut`) || !strings.Contains(body, ">Ctrl K</kbd>") || strings.Contains(body, ">⌘K</kbd>") {
		t.Errorf("search hint is not the Ctrl K default with data-search-shortcut")
	}

	js := s.get(t, "/static/app.js", cookie).Body.String()
	if !strings.Contains(js, "[data-search-shortcut]") || !strings.Contains(js, `"⌘K"`) {
		t.Errorf("app.js does not switch the hint to ⌘K on Apple platforms")
	}
}
