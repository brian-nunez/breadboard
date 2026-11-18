package spec

import (
	"fmt"
	"slices"
)

// validateServiceHost checks an infra service's host access: only types
// with an HTTPS listener are routed, on the catalog's listener.
func validateServiceHost(svc Service, info TypeInfo, p string, add func(error)) {
	if svc.Host == nil {
		return
	}

	hp := join(p, "host")
	if svc.Host.Port != 0 {
		add(Violation{Path: join(hp, "port"), Message: "must be omitted: an infra service is reached on its catalog HTTPS listener"})
	}

	if !info.HTTPSListener {
		add(Violation{Path: hp, Message: fmt.Sprintf("type %s has no HTTPS listener; the host router routes only HTTP over TLS. Reach %s from an app in the lab over its TCP listener, or publish an app's port with ports", svc.Type, svc.Name)})
	}
}

// validateAppHost checks an app's host access: the router needs a TLS port,
// and a transparent pod admits only its declared TLS listeners.
func validateAppHost(app App, p string, add func(error)) {
	if app.Host == nil {
		return
	}

	hp := join(p, "host")
	if port := app.Host.Port; port != 0 {
		pp := join(hp, "port")
		if !isPort(port) {
			add(Violation{Path: pp, Message: "must be between 1 and 65535"})

			return
		}

		if slices.ContainsFunc(app.Sidecars, Sidecar.Transparent) && !slices.Contains(declaredTLSPorts(app), port) {
			add(Violation{Path: pp, Message: fmt.Sprintf("app %s runs in transparent mode, which admits only its declared TLS listeners; use its envoy inbound.port or register.port, or omit port", app.Name)})
		}

		return
	}

	if _, ok := app.TLSPort(); !ok {
		add(Violation{Path: hp, Message: fmt.Sprintf("app %s serves no TLS port: set host.port to the port it serves TLS on, give it an envoy sidecar with inbound, or register with its TLS port", app.Name)})
	}
}

// declaredTLSPorts lists the app's declared TLS listeners: every Envoy
// inbound.port and its register.port.
func declaredTLSPorts(app App) []int {
	var out []int

	for _, sc := range app.Sidecars {
		if sc.Envoy != nil && sc.Envoy.Inbound != nil && sc.Envoy.Inbound.Port != 0 {
			out = append(out, sc.Envoy.Inbound.Port)
		}
	}

	if app.Register != nil && app.Register.Port != 0 {
		out = append(out, app.Register.Port)
	}

	return out
}
