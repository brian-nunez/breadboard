// Package mcp is the MCP wrapper over the core: bb mcp serves the core's
// operations as tools over stdio, using the official Go SDK for the protocol.
// Like the CLI, it parses input, calls one core operation, and renders the
// result; it holds no domain logic.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Options configures a Server.
type Options struct {
	// Dir is the root directory: tools find the experiment from here, and
	// every directory a tool is given must stay inside it. Empty means the
	// working directory.
	Dir string
	// ReadOnly registers only the tools that change nothing a user owns:
	// experiment.yaml, project files, install settings, and containers.
	ReadOnly bool
}

// Server is the MCP server.
type Server struct {
	eng      *core.Engine
	root     string
	readOnly bool
	srv      *sdk.Server
}

// New builds a server and registers its tools.
func New(eng *core.Engine, opts Options) (*Server, error) {
	root := opts.Dir
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		root = wd
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	s := &Server{eng: eng, root: root, readOnly: opts.ReadOnly}
	s.srv = sdk.NewServer(&sdk.Implementation{
		Name:    "breadboard",
		Title:   "Breadboard",
		Version: eng.Version().Version,
	}, &sdk.ServerOptions{
		Instructions: instructions,
		// Only tools: no logging capability, no prompts or resources.
		Capabilities: &sdk.ServerCapabilities{},
	})
	s.addInfoTools()
	s.addSpecTools()
	s.addRunTools()
	return s, nil
}

// Run serves the protocol on t until the client disconnects or ctx ends.
func (s *Server) Run(ctx context.Context, t sdk.Transport) error {
	return s.srv.Run(ctx, t)
}

// Connect starts a session on t and returns without waiting; tests use it
// with in-memory transports.
func (s *Server) Connect(ctx context.Context, t sdk.Transport) (*sdk.ServerSession, error) {
	return s.srv.Connect(ctx, t, nil)
}

const instructions = `Breadboard (bb) runs real system design experiments locally: real services (Kafka, Postgres, Redis, Consul, S3 compatible) in Docker Compose with TLS on by default, plus app code from starters. experiment.yaml is the source of truth; every tool reads or edits it.

Call "guide" first for how bb works and the usual workflow. Typical flow: init, add_service or apply_starter, up, then status, logs, creds and the explorers (postgres_tables, redis_keys, consul_catalog, kafka_topics, s3_buckets) to inspect it, and down when done. Tools act on the experiment containing the directory bb mcp was started in; pass "dir" to choose another one inside it.`

// kind is how a tool affects the system, which sets its annotations and
// whether --read-only keeps it.
type kind int

const (
	// readTool changes nothing.
	readTool kind = iota
	// stateTool may write generated state (.bb/ or the install's CA) the
	// first time, but changes nothing the user owns. Kept in --read-only.
	stateTool
	// writeTool changes experiment.yaml, project files, settings, or
	// containers, without losing anything.
	writeTool
	// destructiveTool can remove something: services, containers, data.
	destructiveTool
)

func annotations(k kind, idempotent bool) *sdk.ToolAnnotations {
	no := false
	a := &sdk.ToolAnnotations{OpenWorldHint: &no, IdempotentHint: idempotent}
	switch k {
	case readTool:
		a.ReadOnlyHint = true
	case stateTool, writeTool:
		a.DestructiveHint = &no
	case destructiveTool:
		yes := true
		a.DestructiveHint = &yes
	}
	return a
}

// add registers a tool, unless the server is read-only and the tool writes.
// The SDK derives the input and output schemas from In and Out, validates
// input, and returns Out as structured content (and as JSON text).
func add[In, Out any](s *Server, k kind, idempotent bool, t *sdk.Tool, h func(context.Context, *sdk.CallToolRequest, In) (Out, error)) {
	if s.readOnly && k >= writeTool {
		return
	}
	t.Annotations = annotations(k, idempotent)
	sdk.AddTool(s.srv, t, func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		out, err := h(ctx, req, in)
		if err != nil {
			var zero Out
			return nil, zero, describe(err)
		}
		return nil, out, nil
	})
}

// dirIn is the input of tools that only need to know which experiment.
type dirIn struct {
	Dir string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
}

// dir resolves a tool's dir argument against the root and refuses anything
// outside it, so a client cannot point bb at arbitrary paths on the host.
func (s *Server) dir(arg string) (string, error) {
	p := arg
	if p == "" {
		return s.root, nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.root, p)
	}
	p = filepath.Clean(p)
	if !s.inside(resolveExisting(p)) {
		return "", fmt.Errorf("%q is outside %s, the directory bb mcp was started in; start bb mcp in (or with -C) a directory that contains it", arg, s.root)
	}
	return p, nil
}

func (s *Server) inside(p string) bool {
	rel, err := filepath.Rel(s.root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolveExisting follows symlinks in the longest existing prefix of p, so a
// link inside the root cannot lead outside it.
func resolveExisting(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// describe turns a core error into the text a model reads: every violation
// with its YAML path, and what to do about the common failures.
func describe(err error) error {
	var ee *eventsError
	if errors.As(err, &ee) {
		return err // already described
	}
	var ve *core.ValidationError
	if errors.As(err, &ve) {
		lines := make([]string, len(ve.Violations))
		for i, v := range ve.Violations {
			lines[i] = "  " + v.String()
		}
		return fmt.Errorf("experiment.yaml is invalid:\n%s", strings.Join(lines, "\n"))
	}
	switch {
	case errors.Is(err, core.ErrNotInitialized):
		return fmt.Errorf("%w (with MCP: call init, or pass dir to point at an experiment)", err)
	case errors.Is(err, core.ErrDockerUnavailable):
		return fmt.Errorf("%w; the user needs to start Docker (Docker Desktop, OrbStack, or the docker daemon)", err)
	case errors.Is(err, core.ErrBusy):
		return fmt.Errorf("%w; another bb command is running on this experiment, wait for it and try again", err)
	case errors.Is(err, core.ErrNewerFormat):
		return fmt.Errorf("%w; upgrade bb: go install github.com/brian-nunez/breadboard/bb@latest", err)
	case errors.Is(err, core.ErrUnhealthy):
		return fmt.Errorf("%w; call logs for the failing service to see why", err)
	}
	return err
}

// progress collects a long operation's events for the tool result and, when
// the client asked for progress with a token, also sends each one as a
// progress notification so the client sees the call is alive.
type progress struct {
	ctx    context.Context
	req    *sdk.CallToolRequest
	mu     sync.Mutex
	events []event
}

func newProgress(ctx context.Context, req *sdk.CallToolRequest) *progress {
	return &progress{ctx: ctx, req: req, events: []event{}}
}

func (p *progress) sink(ev core.Event) {
	p.mu.Lock()
	p.events = append(p.events, eventView(ev))
	n := len(p.events)
	p.mu.Unlock()
	if p.req == nil || p.req.Params == nil || p.req.Session == nil {
		return
	}
	token := p.req.Params.GetProgressToken()
	if token == nil {
		return
	}
	msg := ev.Message
	if ev.Kind == core.EventHealth {
		msg = ev.Service + ": " + ev.Health
	}
	_ = p.req.Session.NotifyProgress(p.ctx, &sdk.ProgressNotificationParams{ProgressToken: token, Progress: float64(n), Message: msg})
}

func (p *progress) list() []event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]event{}, p.events...)
}

// eventsError keeps the steps that ran before a failure, so the model sees
// how far the operation got (which service turned unhealthy, for example).
type eventsError struct {
	err    error
	events []event
}

func withEvents(err error, events []event) error {
	err = describe(err)
	if len(events) == 0 {
		return err
	}
	return &eventsError{err: err, events: events}
}

func (e *eventsError) Unwrap() error { return e.err }

func (e *eventsError) Error() string {
	msg := e.err.Error() + "\n\nProgress before the failure:"
	for _, ev := range e.events {
		switch ev.Kind {
		case string(core.EventHealth):
			msg += "\n  " + ev.Service + ": " + ev.Health
		default:
			msg += "\n  " + ev.Kind + ": " + ev.Message
		}
	}
	return msg
}
