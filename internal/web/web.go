// Package web is the read-only web UI wrapper over the core, served by
// bb serve. Handlers call core operations and render templ components; they
// hold no domain logic and never change the system: every action is shown as
// a command to copy.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
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
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /live/overview", s.overviewLive)
	return s.guard(mux)
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
	for _, svc := range st.Services {
		d.Services = append(d.Services, newRow(svc))
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
