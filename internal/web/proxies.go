package web

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Helpers for proxy pages: upstream modes, and the proxy explorers'
// tables.

// modeName names an upstream TLS mode as the UI shows it.
func modeName(mode string) string {
	switch mode {
	case "off":
		return "terminate"
	case "passthrough":
		return "passthrough"
	case "on":
		return "re-encrypt"
	}
	return mode
}

// modeHelp explains an upstream TLS mode in one line.
func modeHelp(mode string) string {
	switch mode {
	case "off":
		return "decrypted here, plaintext to the backend on a private segment"
	case "passthrough":
		return "forwarded encrypted, routed by SNI; the client sees the backend's certificate"
	case "on":
		return "decrypted here, then TLS to the backend, verified against the lab CA"
	}
	return ""
}

// serviceLink is where a target's page lives: an app with replicas is
// shown by its first instance.
func serviceLink(rows []row, name string) string {
	if insts := instancesOf(rows, name); len(insts) > 0 {
		return "/services/" + insts[0].Name
	}
	return "/services/" + name
}

// proxyTargetLabel is a target with its replica count.
func proxyTargetLabel(u core.UpstreamInfo) string {
	if u.App && u.Instances > 1 {
		return fmt.Sprintf("%s ×%d", u.Service, u.Instances)
	}
	return u.Service
}

// upstreamTitle names a proxy's backend after the upstream it balances.
func upstreamTitle(name string, u core.ProxyUpstream) string {
	if u.Target == "" {
		return name
	}
	return name + " → " + u.Target
}

// haproxyServerStatus colours a server's state.
func haproxyServerStatus(s core.HAProxyServer) status {
	switch {
	case s.Status == "MAINT (resolution)":
		return status{"No address (DNS)", "grey"}
	case strings.HasPrefix(s.Status, "UP") && s.Status != "UP":
		return status{s.Status + " (rising)", "blue"}
	case strings.HasPrefix(s.Status, "DOWN") && s.Status != "DOWN":
		return status{s.Status + " (falling)", "amber"}
	case s.Up && (s.ConnectErrors > 0 || s.ResponseErrors > 0 || s.HTTP5xx > 0):
		return status{"Up, with errors", "amber"}
	case s.Up:
		return status{"Up", "green"}
	case strings.HasPrefix(s.Status, "MAINT"):
		return status{"Maintenance", "grey"}
	}
	return status{cmp.Or(s.Status, "Unknown"), "red"}
}

// haproxyServerName is the replica in a slot, or the slot.
func haproxyServerName(s core.HAProxyServer) string {
	return cmp.Or(s.Instance, s.Address, s.Name)
}

// backendSessions is every session the backend's servers handled.
func backendSessions(b core.HAProxyBackend) int64 {
	var n int64
	for _, s := range b.Servers {
		n += s.TotalSessions
	}
	return n
}

// serverErrors adds a server's connection, response and 5xx errors.
func serverErrors(s core.HAProxyServer) int64 {
	return s.ConnectErrors + s.ResponseErrors + s.HTTP5xx
}

// checkText is a server's last health check.
func checkText(s core.HAProxyServer) string {
	switch {
	case s.CheckStatus == "":
		return "no check"
	case s.CheckDesc != "":
		return fmt.Sprintf("%s · %s · %d ms", s.CheckStatus, s.CheckDesc, s.CheckMillis)
	}
	return fmt.Sprintf("%s · %d ms", s.CheckStatus, s.CheckMillis)
}

// traefikServerStatus colours a Traefik server's health status.
func traefikServerStatus(s core.TraefikServer) status {
	switch s.Status {
	case "UP":
		return status{"Up", "green"}
	case "DOWN":
		return status{"Down", "red"}
	}
	return status{"No health check", "grey"}
}

// traefikTLS describes how a router treats TLS.
func traefikTLS(r core.TraefikRouter) string {
	switch r.TLS {
	case "terminate":
		return "decrypts"
	case "passthrough":
		return "passthrough"
	}
	return "plaintext"
}

// resolvedText is where an NGINX backend's name resolves.
func resolvedText(b core.NginxBackend) string {
	if b.Address == "" {
		return "does not resolve (not running?)"
	}
	return b.Address
}

// envoyName is the Envoy the explorer read: the infra proxy or sidecar.
func envoyName(x *core.EnvoyClusters) string { return cmp.Or(x.Proxy, x.Sidecar) }

// pgbouncerName is the PgBouncer the explorer read.
func pgbouncerName(x *core.PgBouncerPools) string { return cmp.Or(x.Proxy, x.Sidecar) }

// infraTab is the page tab an explorer belongs to: an infra service's
// explore tab, or the sidecar tab.
func infraTab(proxy, sidecarTab string) string {
	if proxy != "" {
		return "explore"
	}
	return sidecarTab
}
