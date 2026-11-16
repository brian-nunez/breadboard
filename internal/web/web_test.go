package web

import (
	"context"
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

func (nullRuntime) Available(context.Context) error                    { return nil }
func (nullRuntime) Up(context.Context, runtime.Project) error          { return nil }
func (nullRuntime) Down(context.Context, runtime.Project, bool) error  { return nil }
func (nullRuntime) RemoveOwned(context.Context, string, bool) error    { return nil }
func (nullRuntime) Subnets(context.Context) ([]runtime.Network, error) { return nil, nil }
func (nullRuntime) Owners(context.Context, string) ([]string, error)   { return nil, nil }
func (nullRuntime) Containers(context.Context, string) ([]runtime.Container, error) {
	return nil, nil
}
func (nullRuntime) Stats(context.Context, []string) (map[string]runtime.Usage, error) {
	return map[string]runtime.Usage{}, nil
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
	t.Cleanup(func() { s.ln.Close() })
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
			s.ln.Close()
			t.Errorf("New(%q) listened", addr)
		}
	}
}

func TestNoExperimentShowsHowToStart(t *testing.T) {
	s := newServer(t)
	rec := s.get(t, "/", map[string]string{"Cookie": signIn(t, s)})
	body := rec.Body.String()
	for _, want := range []string{"No experiment here yet", "bb init kafka-lab", "data-copy"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty overview lacks %q", want)
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
