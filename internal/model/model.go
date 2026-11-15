// Package model resolves a validated spec against the catalog into the
// resolved model: every default applied, edges derived from uses, listeners
// chosen, and private segments computed for plaintext edges. Every output is
// generated from this model.
package model

import (
	"fmt"
	"slices"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// Kind separates catalog infrastructure from user code.
type Kind string

const (
	Infra Kind = "infra"
	App   Kind = "app"
)

// Defaults for app services.
const (
	DefaultAppMemory = "512m"
	DefaultAppCPUs   = "0.5"
	// Used when an app opts into an HTTP health check with `health`.
	DefaultAppHealthPath = "/healthz"
	DefaultAppHealthPort = 8080
)

// Model is a fully resolved experiment.
type Model struct {
	Name     string
	Domain   string
	Project  string // compose project name: bb-<name>
	Services []*Service
	Edges    []Edge
	Segments []Segment
}

// Service is an infra or app service with everything defaulted.
type Service struct {
	Name      string
	Kind      Kind
	FQDN      string
	Identity  string
	Resources catalog.Resources

	// Infra only.
	Entry  *catalog.Entry
	Config map[string]string

	// App only.
	App *spec.App
	// Health is the app's HTTP health check, or nil for the default check
	// that the container's main process is running. Apps need no health code.
	Health *spec.Health
}

// Edge is a directed connection from an app to an infra listener.
type Edge struct {
	From     string
	To       string
	Listener string
	Port     int
	TLS      bool
	Reason   string // why the edge is plaintext
}

// Segment is a private network shared by a plaintext listener and its clients.
type Segment struct {
	Name    string // compose network name, e.g. plain-postgres
	Target  string
	Clients []string
	Reasons []string
}

// SegmentFQDN is a service's alias on its private segment.
func SegmentFQDN(service, domain string) string {
	return fmt.Sprintf("%s.plain.%s", service, domain)
}

// Service returns a service by name.
func (m *Model) Service(name string) (*Service, bool) {
	i := slices.IndexFunc(m.Services, func(s *Service) bool { return s.Name == name })
	if i < 0 {
		return nil, false
	}
	return m.Services[i], true
}

// EdgesFrom lists an app's edges in uses order.
func (m *Model) EdgesFrom(name string) []Edge {
	var out []Edge
	for _, e := range m.Edges {
		if e.From == name {
			out = append(out, e)
		}
	}
	return out
}

// Segment returns the private segment of a target service.
func (m *Model) Segment(target string) (Segment, bool) {
	i := slices.IndexFunc(m.Segments, func(s Segment) bool { return s.Target == target })
	if i < 0 {
		return Segment{}, false
	}
	return m.Segments[i], true
}

// SegmentsOf lists the segments a service belongs to, as target or client.
func (m *Model) SegmentsOf(name string) []Segment {
	var out []Segment
	for _, s := range m.Segments {
		if s.Target == name || slices.Contains(s.Clients, name) {
			out = append(out, s)
		}
	}
	return out
}

// PlaintextEdges lists edges that deliberately skip TLS.
func (m *Model) PlaintextEdges() []Edge {
	var out []Edge
	for _, e := range m.Edges {
		if !e.TLS {
			out = append(out, e)
		}
	}
	return out
}

// Resolve builds the model from a spec that already passed spec.Validate.
func Resolve(s *spec.Spec, domain string) (*Model, error) {
	m := &Model{Name: s.Name, Domain: domain, Project: "bb-" + s.Name}
	for _, svc := range s.Services {
		entry, ok := catalog.Lookup(svc.Type)
		if !ok {
			return nil, fmt.Errorf("resolve %s: unknown type %q", svc.Name, svc.Type)
		}
		m.Services = append(m.Services, &Service{
			Name:      svc.Name,
			Kind:      Infra,
			FQDN:      svc.Name + "." + domain,
			Identity:  pki.Identity(domain, s.Name, svc.Name),
			Resources: resources(svc.Resources, entry.Resources),
			Entry:     entry,
			Config:    entry.ResolveConfig(svc.Config),
		})
	}
	for i := range s.Apps {
		app := &s.Apps[i]
		var health *spec.Health
		if app.Health != nil {
			health = &spec.Health{Path: DefaultAppHealthPath, Port: DefaultAppHealthPort}
			if app.Health.Path != "" {
				health.Path = app.Health.Path
			}
			if app.Health.Port != 0 {
				health.Port = app.Health.Port
			}
		}
		m.Services = append(m.Services, &Service{
			Name:      app.Name,
			Kind:      App,
			FQDN:      app.Name + "." + domain,
			Identity:  pki.Identity(domain, s.Name, app.Name),
			Resources: resources(app.Resources, catalog.Resources{Memory: DefaultAppMemory, CPUs: DefaultAppCPUs}),
			App:       app,
			Health:    health,
		})
		for _, u := range app.Uses {
			target, ok := m.Service(u.Service)
			if !ok || target.Kind != Infra {
				return nil, fmt.Errorf("resolve %s: uses %q, which is not an infra service", app.Name, u.Service)
			}
			l := target.Entry.Listeners[0]
			e := Edge{From: app.Name, To: target.Name, Listener: l.Name, Port: l.Port, TLS: true}
			if u.TLS.Plaintext() {
				if l.PlaintextPort == 0 {
					return nil, fmt.Errorf("resolve %s: %s has no plaintext listener", app.Name, target.Name)
				}
				e.TLS, e.Port, e.Reason = false, l.PlaintextPort, u.TLS.Reason
			}
			m.Edges = append(m.Edges, e)
		}
	}
	m.Segments = segments(m)
	return m, nil
}

// segments groups plaintext edges by target: one private segment per target,
// holding the target and exactly the clients that asked for plaintext.
func segments(m *Model) []Segment {
	var out []Segment
	for _, svc := range m.Services {
		var seg *Segment
		for _, e := range m.Edges {
			if e.TLS || e.To != svc.Name {
				continue
			}
			if seg == nil {
				seg = &Segment{Name: "plain-" + svc.Name, Target: svc.Name}
			}
			seg.Clients = append(seg.Clients, e.From)
			seg.Reasons = append(seg.Reasons, fmt.Sprintf("%s: %s", e.From, e.Reason))
		}
		if seg != nil {
			out = append(out, *seg)
		}
	}
	return out
}

func resources(override spec.Resources, def catalog.Resources) catalog.Resources {
	r := def
	if override.Memory != "" {
		r.Memory = override.Memory
	}
	if override.CPUs != "" {
		r.CPUs = override.CPUs
	}
	return r
}
