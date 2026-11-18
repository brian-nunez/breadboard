// Package web is the read-only web UI wrapper over the core, served by
// bb serve. Handlers call core operations and render templ components; they
// hold no domain logic and never change the system: every action is shown as
// a command to copy.
package web

import (
	"cmp"
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/drivers/memory"

	"github.com/brian-nunez/breadboard/internal/core"
)

//go:generate sh ../../scripts/web.sh generate

//go:embed static
var staticFS embed.FS

// Options configures a Server.
type Options struct {
	// Dir is the project directory; experiment.yaml is found from here.
	Dir string
	// Addr is the listen address; empty means 127.0.0.1 on a free port.
	// Only loopback addresses are accepted.
	Addr string
	// Refresh is how often live pages update; zero means 2 seconds.
	Refresh time.Duration
}

// Server is a running UI.
type Server struct {
	eng      *core.Engine
	dir      string
	token    string
	port     string
	refresh  time.Duration
	sessions *stash.Store
	ln       net.Listener
	http     *http.Server
}

// New listens on a loopback address and returns the server. Call Serve to
// start answering and URL for the address to open.
func New(eng *core.Engine, opts Options) (*Server, error) {
	if opts.Dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}

		opts.Dir = wd
	}

	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", addr, err)
	}

	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("listen address %q: bb serve only listens on a loopback address such as 127.0.0.1", addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	s := &Server{
		eng:      eng,
		dir:      opts.Dir,
		token:    rand.Text(),
		port:     port,
		refresh:  opts.Refresh,
		sessions: memory.New(),
		ln:       ln,
	}
	if s.refresh == 0 {
		s.refresh = 2 * time.Second
	}

	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}

	return s, nil
}

// URL is the address to open. It carries the launch token, which the server
// exchanges for a session cookie on first use.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%s/auth?token=%s", s.port, s.token)
}

// Serve answers requests until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = s.http.Shutdown(shutdown)
	}()

	if err := s.http.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// Handler returns the guarded router.
func (s *Server) Handler() http.Handler {
	return s.guard(s.routes())
}

// routes is the router without the guard.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /dashboard", s.overview)
	mux.HandleFunc("GET /home", s.home)
	mux.HandleFunc("GET /starters", s.startersList)
	mux.HandleFunc("GET /starters/{area}/{name}", s.starter)
	mux.HandleFunc("GET /live/overview", s.overviewLive)
	mux.HandleFunc("GET /architecture", s.architecture)
	mux.HandleFunc("GET /live/architecture", s.architectureLive)
	mux.HandleFunc("GET /services/{name}", s.service)
	mux.HandleFunc("GET /live/logs/{name}", s.logs)
	mux.HandleFunc("GET /fragments/explore/{name}", s.exploreFragment)
	mux.HandleFunc("GET /fragments/live/{name}", s.liveFragment)
	mux.HandleFunc("GET /search-index", s.searchIndex)
	mux.HandleFunc("GET /docs", s.docsHome)
	mux.HandleFunc("GET /docs/{tech}", s.docsTech)
	mux.HandleFunc("GET /docs/{tech}/{guide}", s.docsGuide)

	return mux
}

// overview is the dashboard's front page.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	d := s.dashboard(r.Context())
	render(w, r, overviewPage(d))
}

// overviewLive streams the overview's content as it changes.
func (s *Server) overviewLive(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, func(ctx context.Context) templComponent {
		return overviewBody(s.dashboard(ctx))
	})
}

// architecture shows the graph with one node selected for the inspector.
func (s *Server) architecture(w http.ResponseWriter, r *http.Request) {
	d := s.dashboard(r.Context())
	render(w, r, architecturePage(d, selectedNode(d, r.URL.Query().Get("node"))))
}

func (s *Server) architectureLive(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("node")
	s.stream(w, r, func(ctx context.Context) templComponent {
		d := s.dashboard(ctx)

		return architectureBody(d, selectedNode(d, want))
	})
}

// service is one service's page; ?tab= picks the tab.
func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	p := s.servicePage(r.Context(), r.PathValue("name"), r.URL.Query().Get("tab"))
	if p.Row.Name == "" && p.Problem == "" {
		http.NotFound(w, r)

		return
	}

	render(w, r, serviceView(p))
}

func (s *Server) servicePage(ctx context.Context, name, tab string) servicePage {
	p := servicePage{Dash: s.dashboard(ctx), Tab: tab}
	if p.Dash.Problem != "" || p.Dash.NoExperiment {
		p.Problem = cmp.Or(p.Dash.Problem, "There is no experiment in this directory.")

		return p
	}

	r, ok := find(p.Dash.Services, name)
	if !ok {
		return p
	}

	p.Row = r

	d, err := s.eng.ServiceDetails(ctx, core.ServiceDetailsRequest{Dir: s.dir, Name: name})
	if err != nil {
		p.Problem = err.Error()

		return p
	}

	p.Details = d
	// Learning notes belong to infra catalog entries. A sidecar type may
	// share an infra type's id (envoy), but those notes are not its own.
	if d.Type != "" && d.Kind == "infra" {
		if l, err := s.eng.Learn(ctx, core.LearnRequest{Type: d.Type}); err == nil {
			// Notes are written for any experiment; this page knows which one.
			p.Notes = parseNotes(strings.ReplaceAll(l.Notes, "<experiment>", p.Dash.Experiment))
		}
	}

	if !slices.ContainsFunc(p.tabs(), func(t [2]string) bool { return t[0] == p.Tab }) {
		p.Tab = "overview"
	}

	return p
}

// exploreFragment runs a service's explorer and returns just its content.
// The explorer tab fetches it once after the page renders, because some
// tools (Kafka's JVM-based CLIs) take seconds.
// ?tab= picks the explorer: explore (an infra service's own), envoy, or
// pgbouncer (an app's sidecar, or the sidecar itself).
func (s *Server) exploreFragment(w http.ResponseWriter, r *http.Request) {
	tab := cmp.Or(r.URL.Query().Get("tab"), "explore")

	p := s.servicePage(r.Context(), r.PathValue("name"), tab)
	if p.Details == nil || p.Tab != tab || !slices.Contains([]string{"explore", "envoy", "pgbouncer"}, tab) {
		http.NotFound(w, r)

		return
	}

	var err error

	p.Explore, err = s.explore(r.Context(), tab, p.Details.Type, p.Row.Name)
	if err != nil {
		p.ExploreErr = err.Error()
	}

	render(w, r, exploreResult(p))
}

// explore runs the explorer of a tab: a sidecar's, or the one for an infra
// service's catalog type.
func (s *Server) explore(ctx context.Context, tab, typ, name string) (any, error) {
	req := core.ExploreRequest{Dir: s.dir, Service: name}

	switch tab {
	case "envoy":
		return s.eng.EnvoyClusters(ctx, req)
	case "pgbouncer":
		return s.eng.PgBouncerPools(ctx, req)
	}

	switch typ {
	case "postgres":
		return s.eng.PostgresTables(ctx, req)
	case "redis":
		return s.eng.RedisKeys(ctx, req)
	case "consul":
		return s.eng.ConsulCatalog(ctx, req)
	case "kafka":
		return s.eng.KafkaTopics(ctx, req)
	case "s3-compatible":
		return s.eng.S3Buckets(ctx, req)
	case "haproxy":
		return s.eng.HAProxyStats(ctx, req)
	case "traefik":
		return s.eng.TraefikRoutes(ctx, req)
	case "nginx":
		return s.eng.NginxStatus(ctx, req)
	case "envoy":
		return s.eng.EnvoyClusters(ctx, req)
	case "pgbouncer":
		return s.eng.PgBouncerPools(ctx, req)
	}

	return nil, fmt.Errorf("no explorer for %s", typ)
}

// logs streams a service's log lines as "line" events, starting with the
// last 200.
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)

		return
	}

	lines, err := s.eng.Logs(r.Context(), core.LogsRequest{Dir: s.dir, Service: r.PathValue("name"), Follow: true, Tail: 200})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	flusher.Flush()

	for l, err := range lines {
		text := l.Text
		if err != nil {
			text = "bb: " + err.Error()
		}

		if _, err := fmt.Fprintf(w, "event: line\ndata: %s\n\n", strings.ReplaceAll(text, "\n", " ")); err != nil {
			return
		}

		flusher.Flush()
	}
}

// selectedNode is the requested service if it exists (an app with
// replicas means its first instance), else the first app, else the first
// service.
func selectedNode(d dashboardData, want string) string {
	for _, r := range d.Services {
		if r.Name == want || (r.Kind == "app" && r.App == want && want != "") {
			return r.Name
		}
	}

	for _, r := range d.Services {
		if r.Kind == "app" {
			return r.Name
		}
	}

	if len(d.Services) > 0 {
		return d.Services[0].Name
	}

	return ""
}

// dashboard gathers what every dashboard page shows. Failures become a
// message on the page, never a broken page.
func (s *Server) dashboard(ctx context.Context) dashboardData {
	d := dashboardData{Dir: homeRelative(s.dir), Version: s.eng.Version().Version}

	st, err := s.eng.Status(ctx, core.StatusRequest{Dir: s.dir})
	switch {
	case errors.Is(err, core.ErrNotInitialized):
		d.NoExperiment = true

		return d
	case err != nil:
		d.Problem = err.Error()

		return d
	}

	d.Experiment, d.Domain, d.Warnings = st.Experiment, st.Domain, st.Warnings

	d.HostURLs, d.HostPort = st.HostURLs, st.HostPort
	for _, svc := range st.Services {
		d.Services = append(d.Services, newRow(svc))
	}
	// A plaintext edge makes both of its ends non-TLS in the table.
	for _, from := range st.Services {
		for _, e := range from.Uses {
			if e.TLS {
				continue
			}

			for i := range d.Services {
				// The target by name, or every instance of an app target.
				if d.Services[i].Name == e.To || (d.Services[i].Kind == "app" && d.Services[i].App == e.To) {
					d.Services[i].TLS = false
				}
			}
		}
	}

	return d
}

func render(w http.ResponseWriter, r *http.Request, c templComponent) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// templComponent is anything templ can render.
type templComponent = templ.Component
