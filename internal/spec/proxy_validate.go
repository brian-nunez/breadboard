package spec

import (
	"fmt"
	"slices"
	"strings"

	"github.com/brian-nunez/bkit/gate"
)

// validateProxy checks a service's upstreams, file and static_file. Only proxy types
// (TypeInfo.Proxy set) take them; what their upstreams may be is data on
// the catalog entry.
func validateProxy(s *Spec, types TypeLookup, svc Service, info TypeInfo, p string, add func(error)) {
	px := info.Proxy
	if px == nil {
		if len(svc.Upstreams) > 0 {
			add(Violation{Path: join(p, "upstreams"), Message: fmt.Sprintf("type %s is not a proxy; only proxy types take upstreams", svc.Type)})
		}
		if svc.File != "" {
			add(Violation{Path: join(p, "file"), Message: fmt.Sprintf("type %s is not a proxy; only proxy types take a file", svc.Type)})
		}
		if svc.StaticFile != "" {
			add(Violation{Path: join(p, "static_file"), Message: fmt.Sprintf("type %s is not a proxy; only proxy types with a static configuration take a static_file", svc.Type)})
		}
		return
	}
	for _, f := range []struct{ key, path string }{{"file", svc.File}, {"static_file", svc.StaticFile}} {
		if f.path == "" {
			continue
		}
		fp := join(p, f.key)
		add(gate.Join(
			check(isRelativePath, fp, "must be a path relative to experiment.yaml"),
			check(isUsablePath, fp, "must not contain a colon, comma, or newline, and must not point into .bb"),
		)(f.path))
	}
	if svc.StaticFile != "" && !px.StaticFile {
		add(Violation{Path: join(p, "static_file"), Message: fmt.Sprintf("type %s has no static configuration a static_file can extend", svc.Type)})
	}
	if len(svc.Upstreams) < px.MinUpstreams {
		add(Violation{Path: join(p, "upstreams"), Message: fmt.Sprintf("type %s needs %s; list it under upstreams", svc.Type, upstreamCount(px))})
	}
	if px.MaxUpstreams > 0 && len(svc.Upstreams) > px.MaxUpstreams {
		add(Violation{Path: join(p, "upstreams"), Message: fmt.Sprintf("type %s takes at most %d upstream(s)", svc.Type, px.MaxUpstreams)})
	}
	targets := map[string]string{}
	routes := map[string]string{}
	for i, u := range svc.Upstreams {
		up := fmt.Sprintf("%s.upstreams[%d]", p, i)
		mode := u.Mode()
		if !slices.Contains(px.Modes, mode) {
			add(Violation{Path: join(up, "tls.mode"), Message: fmt.Sprintf("type %s does not support %s upstreams (supported: %s)", svc.Type, mode, strings.Join(px.Modes, ", "))})
		}
		if mode == TLSOff && !notEmpty(u.TLS.Reason) {
			add(Violation{Path: join(up, "tls.reason"), Message: "is required when tls.mode is off; say why this hop is plaintext"})
		}
		if u.Port != 0 {
			add(check(isPort, join(up, "port"), "must be between 1 and 65535")(u.Port))
		}
		if u.Name != "" {
			add(check(IsServiceOK, join(up, "name"), "must be a lowercase DNS label of at most 63 characters")(u.Name))
		}
		if u.Service == "" {
			add(Violation{Path: join(up, "service"), Message: "is required"})
			continue
		}
		if prev, dup := routes[u.Route()]; dup {
			add(Violation{Path: up, Message: fmt.Sprintf("route %s is already used by %s; give one a different name", u.Route(), prev)})
		} else {
			routes[u.Route()] = up
		}
		if prev, dup := targets[u.Service]; dup {
			add(Violation{Path: join(up, "service"), Message: fmt.Sprintf("%s is already an upstream at %s", u.Service, prev)})
			continue
		}
		targets[u.Service] = up
		if u.Service == svc.Name {
			add(Violation{Path: join(up, "service"), Message: "must not be the proxy itself"})
			continue
		}
		if target, ok := s.Service(u.Service); ok {
			validateInfraUpstream(types, svc, px, u, target, up, add)
			continue
		}
		app, ok := s.App(u.Service)
		if !ok {
			add(Violation{Path: join(up, "service"), Message: "must name a service or app"})
			continue
		}
		if px.Targets != nil {
			add(Violation{Path: join(up, "service"), Message: fmt.Sprintf("type %s only takes %s services as upstreams, not apps", svc.Type, strings.Join(px.Targets, " or "))})
			continue
		}
		validateAppUpstream(app, u, up, add)
	}
}

// upstreamCount says how many upstreams, of which kind, a proxy type
// needs: "exactly 1 upstream (a postgres service)", "at least 2 upstreams".
func upstreamCount(px *ProxyInfo) string {
	n := "at least"
	if px.MaxUpstreams == px.MinUpstreams {
		n = "exactly"
	}
	plural := "s"
	if px.MinUpstreams == 1 {
		plural = ""
	}
	out := fmt.Sprintf("%s %d upstream%s", n, px.MinUpstreams, plural)
	if px.Targets != nil {
		out += fmt.Sprintf(" (a %s service)", strings.Join(px.Targets, " or "))
	}
	return out
}

func validateInfraUpstream(types TypeLookup, proxy Service, px *ProxyInfo, u Upstream, target Service, up string, add func(error)) {
	if px.Targets != nil && !slices.Contains(px.Targets, target.Type) {
		add(Violation{Path: join(up, "service"), Message: fmt.Sprintf("type %s only takes %s services as upstreams, not %s", proxy.Type, strings.Join(px.Targets, " or "), target.Type)})
		return
	}
	if u.Port != 0 {
		add(Violation{Path: join(up, "port"), Message: "must be omitted: an infra target is reached on its catalog listener"})
	}
	info, ok := types(target.Type)
	if !ok {
		return // reported on the target itself
	}
	mode := u.Mode()
	if mode == TLSOff && !info.Plaintext {
		add(Violation{Path: join(up, "tls.mode"), Message: fmt.Sprintf("%s (%s) has no plaintext listener", target.Name, target.Type)})
	}
	if px.HTTP && mode != TLSPassthrough && !info.HTTPS {
		add(Violation{Path: join(up, "service"), Message: fmt.Sprintf("%s (%s) does not speak HTTP; route its TLS by SNI with tls.mode: passthrough", target.Name, target.Type)})
	}
}

func validateAppUpstream(app App, u Upstream, up string, add func(error)) {
	tlsPort, hasTLS := app.TLSPort()
	switch u.Mode() {
	case TLSOn, TLSPassthrough:
		if u.Port == 0 && !hasTLS {
			add(Violation{Path: join(up, "service"), Message: fmt.Sprintf("app %s serves no TLS port: set port to the port it serves TLS on, give it an envoy sidecar with inbound, or register with its TLS port", app.Name)})
		}
	case TLSOff:
		if slices.ContainsFunc(app.Sidecars, Sidecar.Transparent) {
			add(Violation{Path: join(up, "tls.mode"), Message: fmt.Sprintf("app %s runs in transparent mode, which admits only its declared TLS listeners; terminate at its envoy sidecar instead (tls.mode: on)", app.Name)})
			return
		}
		port := u.Port
		if port == 0 {
			port = DefaultPlainPort
		}
		if hasTLS && port == tlsPort {
			add(Violation{Path: join(up, "port"), Message: fmt.Sprintf("plaintext port %d is app %s's TLS port; pick another port for the plaintext listener", port, app.Name)})
		}
		if h := app.Health; h != nil {
			hp := h.Port
			if hp == 0 {
				hp = DefaultHealthPort
			}
			if port == hp {
				add(Violation{Path: join(up, "port"), Message: fmt.Sprintf("plaintext port %d is app %s's health port; pick another port for the plaintext listener", port, app.Name)})
			}
		}
	}
}

// validatePlainPorts requires every terminating upstream to one app to use
// the same port: the app binds one plaintext listener on its segment.
func validatePlainPorts(s *Spec, add func(error)) {
	type first struct {
		port int
		path string
	}
	seen := map[string]first{}
	for _, svc := range s.Services {
		for i, u := range svc.Upstreams {
			if u.Mode() != TLSOff {
				continue
			}
			if _, ok := s.App(u.Service); !ok {
				continue
			}
			port := u.Port
			if port == 0 {
				port = DefaultPlainPort
			}
			up := fmt.Sprintf("services.%s.upstreams[%d]", svc.Name, i)
			if prev, ok := seen[u.Service]; ok && prev.port != port {
				add(Violation{Path: join(up, "port"), Message: fmt.Sprintf("app %s already listens for plaintext on port %d (%s); every terminating upstream to one app uses the same port", u.Service, prev.port, prev.path)})
				continue
			}
			if _, ok := seen[u.Service]; !ok {
				seen[u.Service] = first{port, up}
			}
		}
	}
}
