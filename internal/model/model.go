// Package model resolves a validated spec against the catalog into the
// resolved model: every default applied, edges derived from uses, listeners
// chosen, and private segments computed for plaintext edges. Every output is
// generated from this model.
package model

import (
	"fmt"
	"slices"
	"strings"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/catalog/sidecar"
	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// Kind separates catalog infrastructure from user code.
type Kind string

const (
	Infra Kind = "infra"
	App   Kind = "app"
	// Sidecar runs in an app instance's network namespace and uses its
	// certificate: a type from the sidecar catalog, or the agent that
	// registers the app with a service registry (consul-agent).
	Sidecar Kind = "sidecar"
)

// Defaults for app services.
const (
	DefaultAppMemory = "256m"
	DefaultAppCPUs   = "0.5"
	// Used when an app opts into an HTTP health check with `health`.
	DefaultAppHealthPath = "/healthz"
	DefaultAppHealthPort = spec.DefaultHealthPort
	// Sidecars default to their sidecar catalog type's resources.
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
	// Image is the container image: the exact pin for infra and sidecars;
	// empty for apps, which are built from their directory.
	Image string
	// Aliases are the DNS names on the default network and on the
	// service's certificate (infra and apps): the FQDN, plus the app's
	// shared name for a replica.
	Aliases []string

	// Infra only.
	Entry  *catalog.Entry
	Config map[string]string

	// App only.
	App *spec.App
	// Health is the app's HTTP health check, or nil for the default check
	// that the container's main process is running. Apps need no health code.
	Health *spec.Health
	// AppName is the app in experiment.yaml this instance belongs to; it
	// equals Name for an app with one replica.
	AppName string
	// Instance is 1…N for a replica, 0 for an app with one replica.
	Instance int
	// SharedFQDN is <app>.<domain>, an alias on every replica.
	SharedFQDN string
	// Pod is the service name of the instance's namespace holder, or empty
	// when the app keeps its own namespace (no sidecars).
	Pod string
	// Netinit is the service name of the pod's transparent-mode init
	// container, or empty.
	Netinit string
	// Sidecars are the instance's sidecar service names, in spec order
	// (the registration agent last).
	Sidecars []string

	// Sidecar only. For consul-agent, Entry is the registry's catalog entry.
	Parent      string           // the app instance whose namespace it shares
	SidecarType *sidecar.Type    // its sidecar catalog type
	Sidecar     *spec.Sidecar    // the declared entry, defaults applied; nil for consul-agent
	Targets     []sidecar.Target // Envoy outbound targets
	Registry    string           // consul-agent: the registry service it joins
	Register    *spec.Register   // consul-agent: what it registers, defaults applied
}

// Group is the app a service belongs to: AppName for an app instance, the
// name itself otherwise.
func (s *Service) Group() string {
	if s.AppName != "" {
		return s.AppName
	}
	return s.Name
}

// Edge is a directed connection from an app (by its name in
// experiment.yaml) to an infra listener, or through an Envoy sidecar to
// another app or an infra service.
type Edge struct {
	From     string
	To       string
	Listener string
	Port     int
	TLS      bool
	Reason   string // why the edge is plaintext
	// Via is the name (within the app) of the Envoy sidecar whose outbound
	// listener carries the edge; empty for an edge from uses.
	Via string
	// ToApp marks an app-to-app edge.
	ToApp bool
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

// Instances lists an app's instances in order.
func (m *Model) Instances(app string) []*Service {
	var out []*Service
	for _, s := range m.Services {
		if s.Kind == App && s.AppName == app {
			out = append(out, s)
		}
	}
	return out
}

// EdgesFrom lists an app's edges: uses order, then Envoy outbound targets.
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
		fqdn := svc.Name + "." + domain
		m.Services = append(m.Services, &Service{
			Name:      svc.Name,
			Kind:      Infra,
			FQDN:      fqdn,
			Aliases:   []string{fqdn},
			Identity:  pki.Identity(domain, s.Name, svc.Name),
			Resources: resources(svc.Resources, entry.Resources),
			Image:     entry.Image,
			Entry:     entry,
			Config:    entry.ResolveConfig(svc.Config),
		})
	}
	for i := range s.Apps {
		app := &s.Apps[i]
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
		if err := instances(m, s, app); err != nil {
			return nil, err
		}
	}
	// Outbound targets may be apps declared later, so they resolve once
	// every app exists.
	for i := range s.Apps {
		if err := outbound(m, s, &s.Apps[i]); err != nil {
			return nil, err
		}
	}
	logBuckets(m)
	m.Segments = segments(m)
	return m, nil
}

// logBuckets adds every Fluent Bit sidecar's bucket to its S3 compatible
// service's buckets, so the service creates it at startup.
func logBuckets(m *Model) {
	for _, svc := range m.Services {
		if svc.Sidecar == nil || svc.Sidecar.FluentBit == nil {
			continue
		}
		fb := svc.Sidecar.FluentBit
		target, ok := m.Service(fb.To)
		if !ok || target.Kind != Infra {
			continue
		}
		var buckets []string
		if b := target.Config["buckets"]; b != "" {
			buckets = strings.Split(b, ",")
		}
		if !slices.Contains(buckets, fb.Bucket) {
			target.Config["buckets"] = strings.Join(append(buckets, fb.Bucket), ",")
		}
	}
}

// instances adds an app's instances, each followed by its sidecars.
func instances(m *Model, s *spec.Spec, app *spec.App) error {
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
	shared := app.Name + "." + m.Domain
	for n, name := range app.Instances() {
		inst := &Service{
			Name:       name,
			Kind:       App,
			FQDN:       name + "." + m.Domain,
			Aliases:    []string{name + "." + m.Domain},
			Identity:   pki.Identity(m.Domain, s.Name, app.Name), // replicas share one identity
			Resources:  resources(app.Resources, catalog.Resources{Memory: DefaultAppMemory, CPUs: DefaultAppCPUs}),
			App:        app,
			Health:     health,
			AppName:    app.Name,
			SharedFQDN: shared,
		}
		if app.ReplicaCount() > 1 {
			inst.Instance = n + 1
			inst.Aliases = append(inst.Aliases, shared)
		}
		if app.IsPod() {
			inst.Pod = spec.PodName(name)
		}
		m.Services = append(m.Services, inst)
		for _, declared := range app.Sidecars {
			sc := declared.WithDefaults()
			typ, ok := sidecar.Lookup(sc.Type)
			if !ok || typ.Internal {
				return fmt.Errorf("resolve %s: unknown sidecar type %q", app.Name, sc.Type)
			}
			if sc.Transparent() {
				inst.Netinit = spec.NetinitName(name)
			}
			svc := sidecarService(inst, spec.SidecarServiceName(name, sc.ID()), typ, typ.Image)
			svc.Sidecar = &sc
			m.Services = append(m.Services, svc)
		}
		if app.Register != nil {
			agent, err := registrationAgent(m, app, inst)
			if err != nil {
				return err
			}
			m.Services = append(m.Services, agent)
		}
	}
	return nil
}

func sidecarService(inst *Service, name string, typ *sidecar.Type, image string) *Service {
	inst.Sidecars = append(inst.Sidecars, name)
	return &Service{
		Name:        name,
		Kind:        Sidecar,
		FQDN:        inst.FQDN,
		Identity:    inst.Identity,
		Resources:   typ.Resources,
		Image:       image,
		Parent:      inst.Name,
		SidecarType: typ,
	}
}

// registrationAgent resolves the consul-agent that registers an instance
// with the first registry the app uses.
func registrationAgent(m *Model, app *spec.App, inst *Service) (*Service, error) {
	typ, _ := sidecar.Lookup(spec.SidecarConsulAgent)
	for _, u := range app.Uses {
		target, ok := m.Service(u.Service)
		if !ok || target.Kind != Infra || target.Entry.Sidecar == nil {
			continue
		}
		reg := *app.Register
		if reg.Name == "" {
			reg.Name = app.Name
		}
		svc := sidecarService(inst, spec.SidecarName(inst.Name), typ, target.Entry.Image)
		svc.Entry, svc.Registry, svc.Register = target.Entry, target.Name, &reg
		return svc, nil
	}
	return nil, fmt.Errorf("resolve %s: register needs a service registry in uses", app.Name)
}

// outbound resolves an app's Envoy outbound targets into edges and the
// targets each Envoy sidecar renders.
func outbound(m *Model, s *spec.Spec, app *spec.App) error {
	for _, inst := range m.Instances(app.Name) {
		for _, name := range inst.Sidecars {
			sc, _ := m.Service(name)
			if sc.Sidecar == nil || sc.Sidecar.Envoy == nil {
				continue
			}
			for _, o := range sc.Sidecar.Envoy.Outbound {
				t := sidecar.Target{Service: o.Service, Outbound: o}
				e := Edge{From: app.Name, To: o.Service, TLS: true, Via: sc.Sidecar.ID()}
				if target, ok := s.App(o.Service); ok {
					port, ok := target.TLSPort()
					if !ok {
						return fmt.Errorf("resolve %s: outbound target %s serves no TLS port", app.Name, o.Service)
					}
					t.App, t.FQDN, t.Port = true, target.Name+"."+m.Domain, port
					e.ToApp, e.Listener, e.Port = true, "tls", port
				} else if svc, ok := m.Service(o.Service); ok && svc.Kind == Infra {
					l := svc.Entry.Listeners[0]
					t.FQDN, t.Port = svc.FQDN, l.Port
					e.Listener, e.Port = l.Name, l.Port
				} else {
					return fmt.Errorf("resolve %s: outbound target %q is not a service or app", app.Name, o.Service)
				}
				sc.Targets = append(sc.Targets, t)
				if inst.Instance <= 1 {
					m.Edges = append(m.Edges, e) // one edge per app, not per replica
				}
			}
		}
	}
	return nil
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
