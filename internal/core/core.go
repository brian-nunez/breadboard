// Package core is Breadboard's engine API. Every capability is one method on
// Engine, taking a context and a request struct and returning a result struct
// and an error. The CLI, the web UI, and the MCP server are thin wrappers
// over these methods.
//
// The core never prints, reads stdin, parses flags, or exits. Long operations
// report progress through an EventSink, streams are iterators, and failures a
// wrapper must react to are sentinel errors matched with errors.Is.
package core

import (
	"context"
	"errors"
	"iter"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/brian-nunez/breadboard/internal/errs"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

// Sentinel errors. Match them with errors.Is.
var (
	ErrNotInitialized    = errs.ErrNotInitialized
	ErrExists            = errs.ErrExists
	ErrNotFound          = errs.ErrNotFound
	ErrBusy              = errs.ErrBusy
	ErrValidation        = errs.ErrValidation
	ErrNewerFormat       = errs.ErrNewerFormat
	ErrDockerUnavailable = errs.ErrDockerUnavailable
	ErrUnhealthy         = errs.ErrUnhealthy
)

// ValidationError lists every violation with its YAML path. It matches ErrValidation.
type ValidationError = spec.ValidationError

// Violation is one broken rule.
type Violation = spec.Violation

// Runtime is how the core drives Docker. internal/runtime implements it with
// the docker CLI; tests use a fake.
type Runtime interface {
	Available(ctx context.Context) error
	Up(ctx context.Context, p runtime.Project) error
	Down(ctx context.Context, p runtime.Project, volumes bool) error
	RemoveOwned(ctx context.Context, experimentID string, volumes bool) error
	Containers(ctx context.Context, experiment string) ([]runtime.Container, error)
	Stats(ctx context.Context, ids []string) (map[string]runtime.Usage, error)
	// StatsStream yields usage rows for every running container until ctx
	// ends (StartStatsSampler).
	StatsStream(ctx context.Context) iter.Seq2[runtime.Sample, error]
	Logs(ctx context.Context, p runtime.Project, services []string, follow bool, tail int) iter.Seq2[runtime.LogLine, error]
	Subnets(ctx context.Context) ([]runtime.Network, error)
	Owners(ctx context.Context, experiment string) ([]string, error)
	Exec(ctx context.Context, experiment, service string, cmd []string) ([]byte, error)
	Build(ctx context.Context, p runtime.Project, services []string) error
	ImageUser(ctx context.Context, p runtime.Project, service string) (runtime.ImageUser, error)
	PublishedPorts(ctx context.Context) ([]runtime.PublishedPort, error)
}

// Options configures an Engine. Zero values pick production defaults.
type Options struct {
	// Home is the install home; empty means $BB_HOME or the user config dir.
	Home string
	// Runtime drives Docker; nil means the docker CLI.
	Runtime Runtime
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Version overrides the build version.
	Version string
	// SystemRoots returns public root certificates (PEM) for the trust
	// bundle; nil reads the host's CA bundle.
	SystemRoots func() []byte
	// HealthTimeout bounds how long Up waits for every service to be healthy.
	HealthTimeout time.Duration
	// PollInterval is how often Up checks health.
	PollInterval time.Duration
	// StartupSettle is how long every service must stay healthy, without
	// restarting, before Up reports success.
	StartupSettle time.Duration
	// PortFree reports whether a TCP port binds on 127.0.0.1 now, for the
	// host router's port; nil tries to listen on it.
	PortFree func(port int) bool
}

// Engine is the Breadboard core.
type Engine struct {
	home          state.Home
	rt            Runtime
	now           func() time.Time
	version       string
	systemRoots   func() []byte
	healthTimeout time.Duration
	pollInterval  time.Duration
	startupSettle time.Duration
	portFree      func(port int) bool
	// sampler holds the latest usage per container while a stats
	// sampler runs (StartStatsSampler); nil otherwise.
	sampler atomic.Pointer[usageCache]
}

// New returns an Engine.
func New(opts Options) (*Engine, error) {
	e := &Engine{
		rt:            opts.Runtime,
		now:           opts.Now,
		version:       opts.Version,
		systemRoots:   opts.SystemRoots,
		healthTimeout: opts.HealthTimeout,
		pollInterval:  opts.PollInterval,
		startupSettle: opts.StartupSettle,
		portFree:      opts.PortFree,
	}
	if opts.Home != "" {
		e.home = state.Home{Dir: opts.Home}
	} else {
		dir, err := state.DefaultHomeDir()
		if err != nil {
			return nil, err
		}
		e.home = state.Home{Dir: dir}
	}
	if e.rt == nil {
		e.rt = runtime.New()
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.version == "" {
		e.version = buildVersion()
	}
	if e.systemRoots == nil {
		e.systemRoots = hostRoots
	}
	if e.healthTimeout == 0 {
		e.healthTimeout = 5 * time.Minute
	}
	if e.pollInterval == 0 {
		e.pollInterval = 2 * time.Second
	}
	if e.portFree == nil {
		e.portFree = loopbackPortFree
	}
	if e.startupSettle == 0 {
		e.startupSettle = 5 * time.Second
	}
	return e, nil
}

// EventKind classifies progress events.
type EventKind string

const (
	EventStep    EventKind = "step"    // a pipeline step started
	EventHealth  EventKind = "health"  // a service's health changed
	EventWarning EventKind = "warning" // something the user should know
	EventInfo    EventKind = "info"
)

// Event is one progress report.
type Event struct {
	Kind    EventKind
	Message string
	Service string // set for health events
	Health  string // the new health, for health events
}

// EventSink receives progress events. It may be nil.
type EventSink func(Event)

func (s EventSink) emit(ev Event) {
	if s != nil {
		s(ev)
	}
}

func (s EventSink) step(msg string) { s.emit(Event{Kind: EventStep, Message: msg}) }

func (s EventSink) warn(msg string) { s.emit(Event{Kind: EventWarning, Message: msg}) }

// VersionInfo describes the running build.
type VersionInfo struct {
	Version   string
	GoVersion string
}

// Version reports the build version.
func (e *Engine) Version() VersionInfo {
	info := VersionInfo{Version: e.version}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion
	}
	return info
}

func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" || bi.Main.Version == "(devel)" {
		return "devel"
	}
	return bi.Main.Version
}

// hostCABundles are the PEM bundles Go itself checks on macOS and Linux hosts.
var hostCABundles = []string{
	"/etc/ssl/cert.pem",                                 // macOS, Alpine, BSD
	"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // RHEL 7+
	"/etc/ssl/ca-bundle.pem",                            // openSUSE
}

func hostRoots() []byte {
	for _, p := range hostCABundles {
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			return data
		}
	}
	return nil
}

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
