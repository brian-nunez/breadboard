package core

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/pki"
)

// ServiceDetailsRequest asks about one service.
type ServiceDetailsRequest struct {
	Dir  string
	Name string
}

// ServiceDetails describes how a service is set up: what it is, how apps
// reach it, the environment involved, and its certificate. It is read-only:
// it takes no lock, generates nothing, and never returns a secret value.
type ServiceDetails struct {
	Name    string
	Kind    string // infra, app, or sidecar
	Type    string // catalog type for infra and sidecars
	Title   string // catalog title for infra and sidecars
	Summary string // catalog summary for infra and sidecars
	Image   string
	FQDN    string
	Address string
	// Listeners are an infra service's ports.
	Listeners []Listener
	// UsedBy lists the services that connect to this one.
	UsedBy []string
	// Env is, for infra, the variables every app that uses it receives (one
	// group); for an app, its whole environment. Secret values are empty with
	// Secret set: bb creds shows them.
	Env []EnvGroup
	// Certificate is the service's leaf (an app's, for its sidecar). Nil
	// before the first bb up.
	Certificate *Certificate
	// Apps only.
	Path    string
	Command []string
	// Sidecars only.
	Parent   string
	Registry string
}

// Listener is a port a service accepts connections on.
type Listener struct {
	Name          string
	Protocol      string
	Port          int
	PlaintextPort int // zero when the service has no plaintext listener
}

// EnvGroup is a block of environment variables: Breadboard's own (Service
// empty) or one service's env contract.
type EnvGroup struct {
	Service   string
	Title     string
	Plaintext bool
	Reason    string
	Vars      []EnvVar
}

// Certificate summarises a leaf certificate.
type Certificate struct {
	Issuer   string
	DNSNames []string
	Identity string // SPIFFE URI
	NotAfter time.Time
}

// ServiceDetails returns how a service is set up.
func (e *Engine) ServiceDetails(ctx context.Context, req ServiceDetailsRequest) (*ServiceDetails, error) {
	p, err := e.open(req.Dir)
	if err != nil {
		return nil, err
	}
	st, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}
	m, err := model.Resolve(p.spec, st.Domain)
	if err != nil {
		return nil, err
	}
	svc, ok := m.Service(req.Name)
	if !ok {
		return nil, fmt.Errorf("service %q: %w", req.Name, ErrNotFound)
	}
	// Secret values never leave the core here; only whether a value is one.
	masked := func(string) func(string) string { return func(string) string { return "" } }
	subnets := map[string]netip.Prefix{}
	if ps, err := p.LoadState(); err == nil {
		for name, cidr := range ps.Segments {
			if prefix, err := netip.ParsePrefix(cidr); err == nil {
				subnets[name] = prefix
			}
		}
	}

	d := &ServiceDetails{Name: svc.Name, Kind: string(svc.Kind), FQDN: svc.FQDN, Address: listenerAddress(svc)}
	for _, other := range m.Services {
		uses := slices.ContainsFunc(m.EdgesFrom(other.Name), func(e model.Edge) bool { return e.To == svc.Name })
		if uses || (other.Kind == model.Sidecar && other.Registry == svc.Name) {
			d.UsedBy = append(d.UsedBy, other.Name)
		}
	}
	switch svc.Kind {
	case model.Infra, model.Sidecar:
		d.Type, d.Title, d.Summary, d.Image = svc.Entry.ID, svc.Entry.Title, svc.Entry.Summary, svc.Entry.Image
	}
	switch svc.Kind {
	case model.Infra:
		for _, l := range svc.Entry.Listeners {
			d.Listeners = append(d.Listeners, Listener{Name: l.Name, Protocol: l.Protocol, Port: l.Port, PlaintextPort: l.PlaintextPort})
		}
		// What a TLS client of this service receives.
		g := EnvGroup{Service: svc.Name, Title: svc.Entry.Title}
		for _, v := range svc.Entry.Client(catalog.Client{Service: svc.Name, FQDN: svc.FQDN, Config: svc.Config, Secret: masked(svc.Name)}) {
			g.Vars = append(g.Vars, EnvVar{Name: v.Name, Value: v.Value, Secret: v.Secret})
		}
		d.Env = []EnvGroup{g}
	case model.App:
		d.Path, d.Command = svc.App.Path, svc.App.Command
		for _, g := range compose.AppEnv(m, svc, masked, subnets) {
			eg := EnvGroup{Service: g.Service, Title: g.Title, Plaintext: g.Plaintext, Reason: g.Reason}
			for _, v := range g.Vars {
				eg.Vars = append(eg.Vars, EnvVar{Name: v.Name, Value: v.Value, Secret: v.Secret})
			}
			d.Env = append(d.Env, eg)
		}
	case model.Sidecar:
		d.Parent, d.Registry = svc.Parent, svc.Registry
	}
	for i := range d.Env {
		for j := range d.Env[i].Vars {
			if d.Env[i].Vars[j].Secret {
				d.Env[i].Vars[j].Value = ""
			}
		}
	}

	owner := svc.Name
	if svc.Kind == model.Sidecar {
		owner = svc.Parent
	}
	if certPEM, _, ok, err := p.ReadCert(owner); err == nil && ok {
		if cert, err := pki.ParseCert(certPEM); err == nil {
			c := &Certificate{Issuer: cert.Issuer.CommonName, DNSNames: cert.DNSNames, NotAfter: cert.NotAfter}
			if len(cert.URIs) > 0 {
				c.Identity = cert.URIs[0].String()
			}
			d.Certificate = c
		}
	}
	return d, nil
}
