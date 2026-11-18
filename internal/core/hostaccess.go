package core

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"strconv"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

// Host access (ADR-0017): the host router's port and host certificate.

// The host router's port range on 127.0.0.1 (D-190).
const (
	hostPortFirst = 44300
	hostPortCount = 700
)

// hostCertKey is the host certificate's record in state.json's
// certificates, beside the router's lab leaf under its service name.
const hostCertKey = spec.HostRouterName + "/" + state.HostCert

// HostURL is one URL that reaches a lab target from the host.
type HostURL struct {
	URL string
	// Target is the app or service in experiment.yaml.
	Target string
	// Instance is the app instance's service name for a URL that reaches
	// one replica alone (api-2); empty for the target's URL.
	Instance string
}

// hostPortStart is where a lab's port search begins: 44300 plus the FNV-1a
// hash of its name, modulo the range, so labs tend to keep apart.
func hostPortStart(experiment string) int {
	h := fnv.New32a()
	h.Write([]byte(experiment))

	return hostPortFirst + int(h.Sum32()%hostPortCount)
}

// loopbackPortFree reports whether port binds on 127.0.0.1 now.
func loopbackPortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}

	_ = l.Close()

	return true
}

// hostPort returns the port the host router is published on, recording it
// in ps. A lab without a router keeps its recorded port and gets zero.
// Without probe (export), it is the recorded port, or the start port when
// none is recorded, and nothing is recorded. With probe (up), the recorded
// port is kept while it is free or held by this lab's own running router
// (a stopped one only reserves it, so the port must still bind);
// otherwise the search starts again at the lab's start port and wraps,
// taking the first port that binds on 127.0.0.1 and that no container
// publishes, and a move is reported as a warning.
func (e *Engine) hostPort(ctx context.Context, m *model.Model, ps *state.ProjectState, probe bool, ev EventSink) (int, error) {
	if _, ok := m.HostRouter(); !ok {
		return 0, nil
	}

	if !probe {
		if ps.HostPort != 0 {
			return ps.HostPort, nil
		}

		return hostPortStart(m.Name), nil
	}

	published, err := e.rt.PublishedPorts(ctx)
	if err != nil {
		return 0, err
	}

	free := func(port int) bool {
		held := false

		for _, pp := range published {
			if pp.HostPort != port {
				continue
			}

			if pp.ExperimentID != "" && pp.ExperimentID == ps.ExperimentID && pp.Service == spec.HostRouterName {
				// This lab's own router: running, it holds the port;
				// stopped, it only reserves it, and the bind check
				// decides whether another program took the port since.
				held = held || pp.Running

				continue
			}

			return false
		}
		// This lab's running router holds it: binding would fail only
		// because of the router itself.
		return held || e.portFree(port)
	}
	if ps.HostPort != 0 && free(ps.HostPort) {
		return ps.HostPort, nil
	}

	start := hostPortStart(m.Name) - hostPortFirst
	for i := range hostPortCount {
		port := hostPortFirst + (start+i)%hostPortCount
		if port == ps.HostPort || !free(port) {
			continue
		}

		if ps.HostPort != 0 {
			ev.warn(fmt.Sprintf("host port %d is taken; host access moved to %d", ps.HostPort, port))
		}

		ps.HostPort = port

		return port, nil
	}

	return 0, fmt.Errorf("no free host port from %d to %d on 127.0.0.1 for host access", hostPortFirst, hostPortFirst+hostPortCount-1)
}

// reconcileHostCert keeps the host router's host certificate in step with
// the lab's host names (D-188): issued by the host CA for exactly those
// names, reissued when they change or it nears expiry, and removed with
// its state record when the lab has no host target. The host CA is
// loaded, or created, only when the lab has one.
func (e *Engine) reconcileHostCert(p *project, m *model.Model, ps *state.ProjectState, ev EventSink) (*compose.Cert, error) {
	router, ok := m.HostRouter()
	if !ok {
		if err := p.RemoveNamedCert(spec.HostRouterName, state.HostCert); err != nil {
			return nil, err
		}

		delete(ps.Certificates, hostCertKey)

		return nil, nil
	}

	ca, created, err := e.ensureHostCA()
	if err != nil {
		return nil, err
	}

	if created {
		ev.emit(Event{Kind: EventInfo, Message: fmt.Sprintf("Created the Breadboard host CA for *.%s in %s", pki.HostDomain, e.home.CADir())})
	}

	names := catalog.HostNames(router.HostTargets)
	now := e.now()

	certPEM, keyPEM, ok, err := p.ReadNamedCert(router.Name, state.HostCert)
	if err != nil {
		return nil, err
	}

	if ok {
		cert, err := pki.ParseCert(certPEM)
		if err == nil && pki.KeyMatches(cert, keyPEM) && !ca.HostNeedsReissue(cert, names, now) {
			return &compose.Cert{Cert: certPEM, Key: keyPEM}, nil
		}
	}

	leaf, err := ca.IssueHost(names, now)
	if err != nil {
		return nil, fmt.Errorf("issue the host certificate: %w", err)
	}

	if err := p.WriteNamedCert(router.Name, state.HostCert, leaf.CertPEM, leaf.KeyPEM); err != nil {
		return nil, err
	}

	ps.Certificates[hostCertKey] = state.CertRecord{Issuer: hostCAName, Serial: leaf.Cert.SerialNumber.Text(16), NotAfter: leaf.Cert.NotAfter}

	return &compose.Cert{Cert: leaf.CertPEM, Key: leaf.KeyPEM}, nil
}

// hostURL is the URL of a host name on the router's port.
func hostURL(name string, port int) string {
	return fmt.Sprintf("https://%s:%d", name, port)
}

// hostURLs lists every host URL of the lab in route order: each target's
// URL, then its instances'. Nil without a router or a known port.
func hostURLs(m *model.Model, port int) []HostURL {
	router, ok := m.HostRouter()
	if !ok || port == 0 {
		return nil
	}

	var out []HostURL
	for _, t := range router.HostTargets {
		out = append(out, HostURL{URL: hostURL(t.Name, port), Target: t.Target})
		for _, inst := range t.Instances {
			if inst.Name != "" {
				out = append(out, HostURL{URL: hostURL(inst.Name, port), Target: t.Target, Instance: inst.Service})
			}
		}
	}

	return out
}

// serviceHostURLs are the host URLs of one service row: an app instance's
// app URL, then its own URL when the app has replicas; an infra target's
// URL; nil otherwise.
func serviceHostURLs(m *model.Model, svc *model.Service, port int) []string {
	if svc.Kind == model.Sidecar {
		return nil
	}

	var out []string

	for _, u := range hostURLs(m, port) {
		if u.Target == svc.Group() && (u.Instance == "" || u.Instance == svc.Name) {
			out = append(out, u.URL)
		}
	}

	return out
}

// recordedHostPort is the host port recorded in state.json, or zero.
func recordedHostPort(p *project) int {
	ps, err := p.LoadState()
	if err != nil {
		return 0
	}

	return ps.HostPort
}
