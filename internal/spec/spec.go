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

// TLS modes an edge may request.
const (
	TLSOn  = "on"
	TLSOff = "off"
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
}

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
	Resources Resources
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
