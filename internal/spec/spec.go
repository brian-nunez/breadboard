// Package spec reads, validates, edits, and upgrades experiment.yaml, the
// source of truth for an experiment.
//
// The file is kept as a YAML node tree so edits preserve the user's comments
// and key order. Decoding walks that tree directly, which lets every problem
// be reported with its YAML path instead of stopping at the first one.
package spec

import (
	"fmt"
	"slices"
	"strings"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// FileName is the spec's file name inside a project directory.
const FileName = "experiment.yaml"

// CurrentVersion is the newest spec version this build reads and writes.
const CurrentVersion = 1

// TLS modes an edge may request. A proxy's upstream may also ask for
// passthrough: the proxy routes the client's TLS session by SNI without
// decrypting it.
const (
	TLSOn          = "on"
	TLSOff         = "off"
	TLSPassthrough = "passthrough"
)

// Spec is the decoded experiment.yaml.
type Spec struct {
	Version  int
	Name     string
	Services []Service
	Apps     []App
}

// Service is an infra service: an instance of a catalog entry.
type Service struct {
	Name      string
	Type      string
	Resources Resources
	Config    map[string]string
	// Upstreams are a proxy's backends, in spec order (the first is the
	// default route). Only proxy types take them.
	Upstreams []Upstream
	// File is a user-owned native config, relative to experiment.yaml,
	// that replaces a proxy's generated one.
	File string
}

// Upstream is one backend of a proxy: an edge from the proxy to an app (all
// its replicas) or to an infra service, with its own TLS mode.
type Upstream struct {
	Service string
	// Port is the target port; zero means the default for the mode: the
	// target's TLS port (on, passthrough) or its plaintext port (off).
	Port int
	// Name is the route name; empty means Service.
	Name string
	// TLS is the hop's mode: on (re-encrypt, the default), off (terminate
	// at the proxy; plaintext on a private segment) or passthrough.
	TLS TLS
}

// Route is the upstream's route name: Name, or the target's name.
func (u Upstream) Route() string {
	if u.Name != "" {
		return u.Name
	}
	return u.Service
}

// Mode is the upstream's TLS mode with the default applied.
func (u Upstream) Mode() string {
	if u.TLS.Mode == "" {
		return TLSOn
	}
	return u.TLS.Mode
}

// TerminateTargets lists the apps a proxy reaches over a terminate
// (tls.mode: off) hop. Each runs as a pod with an init container that
// confines its plaintext port to its private segment.
func TerminateTargets(s *Spec) map[string]bool {
	out := map[string]bool{}
	for _, svc := range s.Services {
		for _, u := range svc.Upstreams {
			if _, ok := s.App(u.Service); ok && u.Mode() == TLSOff {
				out[u.Service] = true
			}
		}
	}
	return out
}

// DefaultPlainPort is the port an app listens on for plaintext from a
// terminating proxy when the upstream sets none.
const DefaultPlainPort = 8080

// Resources overrides a service's default limits. Empty fields keep the default.
type Resources struct {
	Memory string
	CPUs   string
}

// App is user code built from a directory with a Dockerfile.
type App struct {
	Name      string
	Path      string
	Command   []string
	Uses      []Use
	Ports     []int
	Health    *Health
	Register  *Register
	Resources Resources
	// Replicas is the number of instances; zero means one.
	Replicas int
	// Sidecars share the app's network namespace and a directory with it.
	Sidecars []Sidecar
}

// Use is one entry of an app's uses list: an edge to an infra service.
type Use struct {
	Service string
	TLS     TLS
}

// TLS is an edge's TLS override. The zero value means the default (TLS on).
type TLS struct {
	Mode   string
	Reason string
}

// Plaintext reports whether the edge explicitly turned TLS off.
func (t TLS) Plaintext() bool { return t.Mode == TLSOff }

// Health overrides an app's default health check.
type Health struct {
	Path string
	Port int
}

// Register asks Breadboard to register an app in a service registry (Consul)
// through a sidecar agent, so the app's code does not register itself.
type Register struct {
	Name  string // service name in the registry; empty means the app's name
	Port  int    // the port the app serves on
	Check string // path of an HTTPS health check; empty means a TCP check
}

// DefaultHealthPort is the port of an app's HTTP health check when
// health sets none.
const DefaultHealthPort = 8080

// SidecarName is the name of the sidecar service that registers an app
// instance: <instance>-agent.
func SidecarName(instance string) string { return instance + "-agent" }

// Service returns the infra service with the given name.
func (s *Spec) Service(name string) (Service, bool) {
	i := slices.IndexFunc(s.Services, func(v Service) bool { return v.Name == name })
	if i < 0 {
		return Service{}, false
	}
	return s.Services[i], true
}

// App returns the app with the given name.
func (s *Spec) App(name string) (App, bool) {
	i := slices.IndexFunc(s.Apps, func(v App) bool { return v.Name == name })
	if i < 0 {
		return App{}, false
	}
	return s.Apps[i], true
}

// ServicesOfType returns the names of infra services of a catalog type, in spec order.
func (s *Spec) ServicesOfType(typ string) []string {
	var out []string
	for _, svc := range s.Services {
		if svc.Type == typ {
			out = append(out, svc.Name)
		}
	}
	return out
}

// Violation is one broken rule, located by its YAML path.
type Violation struct {
	Path    string
	Message string
}

func (v Violation) String() string {
	if v.Path == "" {
		return v.Message
	}
	return v.Path + ": " + v.Message
}

// ValidationError lists every violation found. It matches errs.ErrValidation.
type ValidationError struct {
	Violations []Violation
}

func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		lines[i] = v.String()
	}
	return fmt.Sprintf("%s:\n  %s", errs.ErrValidation, strings.Join(lines, "\n  "))
}

// Is makes errors.Is(err, errs.ErrValidation) true.
func (e *ValidationError) Is(target error) bool { return target == errs.ErrValidation }

// violations converts a list into an error, or nil when the list is empty.
func violations(vs []Violation) error {
	if len(vs) == 0 {
		return nil
	}
	return &ValidationError{Violations: vs}
}

// Invalid builds a validation error with a single violation.
func Invalid(path, format string, args ...any) error {
	return &ValidationError{Violations: []Violation{{Path: path, Message: fmt.Sprintf(format, args...)}}}
}
