package core

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/brian-nunez/breadboard/internal/catalog/sidecar"
	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// SidecarInfo describes one sidecar as declared: what it is, the ports it
// binds inside its app's pod, and the environment it gives the app. Secret
// values are empty with Secret set.
type SidecarInfo struct {
	Name    string // within the app: envoy, or the declared name
	Service string // the container's service name: api-envoy, api-2-envoy
	Type    string // sidecar catalog type: envoy, pgbouncer, fluent-bit, consul-agent
	Title   string
	Summary string
	Image   string
	// Ports are what the sidecar listens on in the pod's network namespace.
	Ports []SidecarPort
	// Env lists the variables the sidecar adds to the app's environment;
	// Rewrites the app variables it points at itself (a pooler's
	// DATABASE_URL), with their new values.
	Env      []EnvVar
	Rewrites []EnvVar
	// Settings are the declared knobs, defaults applied, for display.
	Settings []SidecarSetting
	// Envoy only: transparent capture and its egress policy (allow, deny).
	Transparent bool
	Egress      string
	// Targets are an Envoy sidecar's outbound targets (apps or services).
	Targets []string
	// Registry is the registry a consul-agent joins.
	Registry string
}

// SidecarPort is a port a sidecar binds inside the pod.
type SidecarPort struct {
	Port int
	// Loopback ports are bound on 127.0.0.1: only the app and its other
	// sidecars reach them, in plaintext.
	Loopback bool
	Purpose  string
}

// SidecarSetting is one declared knob of a sidecar.
type SidecarSetting struct {
	Name  string
	Value string
}

// consulAgentPorts mirrors the ports spec reserves for a registration
// agent (Serf LAN gossip and its loopback HTTPS API).
var consulAgentPorts = []SidecarPort{
	{Port: 8301, Purpose: "Serf LAN gossip with the Consul server"},
	{Port: 8501, Loopback: true, Purpose: "agent HTTPS API"},
}

// sidecarInfos describes every sidecar of an app instance, in its order.
func sidecarInfos(m *model.Model, inst *model.Service) ([]SidecarInfo, error) {
	if len(inst.Sidecars) == 0 {
		return nil, nil
	}
	masked := func(string) func(string) string { return func(string) string { return "" } }
	groups, err := compose.AppEnv(m, inst, masked, nil)
	if err != nil {
		return nil, err
	}
	lookup := func(name string) (EnvVar, bool) {
		for _, g := range groups {
			for _, v := range g.Vars {
				if v.Name == name {
					return envVar(v.Name, v.Value, v.Secret), true
				}
			}
		}
		return EnvVar{}, false
	}
	var out []SidecarInfo
	for _, name := range inst.Sidecars {
		sc, ok := m.Service(name)
		if !ok {
			continue
		}
		info := sidecarInfo(sc)
		for _, g := range groups {
			if !g.Sidecar || g.Service != name {
				continue
			}
			for _, v := range g.Vars {
				info.Env = append(info.Env, envVar(v.Name, v.Value, v.Secret))
			}
			for _, n := range g.Rewrites {
				if v, ok := lookup(n); ok {
					info.Rewrites = append(info.Rewrites, v)
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// envVar masks a secret value: it never leaves the core here.
func envVar(name, value string, secret bool) EnvVar {
	if secret {
		value = ""
	}
	return EnvVar{Name: name, Value: value, Secret: secret}
}

// sidecarInfo describes a sidecar's type, ports, and settings (not its
// environment, which needs the whole instance).
func sidecarInfo(sc *model.Service) SidecarInfo {
	info := SidecarInfo{Service: sc.Name, Image: sc.Image, Registry: sc.Registry}
	if t := sc.SidecarType; t != nil {
		info.Type, info.Title, info.Summary = t.ID, t.Title, t.Summary
	}
	if sc.Sidecar != nil {
		info.Name = sc.Sidecar.ID()
	} else {
		info.Name = strings.TrimPrefix(sc.Name, sc.Parent+"-")
	}
	set := func(name, value string) {
		info.Settings = append(info.Settings, SidecarSetting{Name: name, Value: value})
	}
	switch {
	case sc.Sidecar != nil && sc.Sidecar.Envoy != nil:
		e := sc.Sidecar.Envoy
		info.Ports = append(info.Ports, SidecarPort{Port: spec.EnvoyAdminPort, Loopback: true, Purpose: "admin API: health check and the Envoy explorer"})
		if i := e.Inbound; i != nil {
			info.Ports = append(info.Ports, SidecarPort{Port: i.Port, Purpose: fmt.Sprintf("inbound TLS with the app's certificate, forwarded to the app on 127.0.0.1:%d", i.AppPort)})
		}
		for _, t := range sc.Targets {
			info.Targets = append(info.Targets, t.Service)
			info.Ports = append(info.Ports, SidecarPort{Port: t.Outbound.Port, Loopback: true, Purpose: fmt.Sprintf("outbound to %s over TLS :%d", t.Service, t.Port)})
			knobs := fmt.Sprintf("lb %s, timeout %s", t.Outbound.LB, t.Outbound.Timeout)
			if t.Outbound.Retries != nil {
				knobs += fmt.Sprintf(", retries %d", *t.Outbound.Retries)
			}
			if o := t.Outbound.Outlier; o != nil {
				knobs += fmt.Sprintf(", outlier ejection after %d consecutive 5xx for %s (at most %d%% of hosts, swept every %s)", o.Consecutive5xx, o.Ejection, o.MaxEjectionPercent, o.Interval)
			}
			set("outbound "+t.Service, knobs)
		}
		if tr := e.Transparent; tr != nil {
			info.Transparent, info.Egress = true, tr.Egress
			info.Ports = append(info.Ports,
				SidecarPort{Port: spec.EnvoyOutboundPort, Purpose: fmt.Sprintf("transparent: the app's redirected outbound traffic (egress %s)", tr.Egress)},
				SidecarPort{Port: spec.EnvoyInboundPort, Purpose: "transparent: redirected inbound traffic, forwarded to the app on loopback"},
			)
			set("transparent", "egress "+tr.Egress)
		}
		if e.File != "" {
			set("config", e.File+" (your own Envoy configuration replaces the generated one)")
		}
	case sc.Sidecar != nil && sc.Sidecar.PgBouncer != nil:
		p := sc.Sidecar.PgBouncer
		info.Targets = []string{p.Service}
		info.Ports = []SidecarPort{{Port: p.Port, Loopback: true, Purpose: fmt.Sprintf("connection pool in front of %s (TLS verify-full to Postgres)", p.Service)}}
		set("service", p.Service)
		set("pool mode", p.PoolMode)
		set("pool size", strconv.Itoa(p.PoolSize))
	case sc.Sidecar != nil && sc.Sidecar.FluentBit != nil:
		f := sc.Sidecar.FluentBit
		info.Targets = []string{f.To}
		info.Ports = []SidecarPort{{Port: spec.FluentBitHTTPPort, Loopback: true, Purpose: "HTTP status: health check"}}
		set("ships", sidecar.LogDir+" to "+f.To)
		set("bucket", f.Bucket)
	case sc.Register != nil:
		info.Ports = consulAgentPorts
		info.Targets = []string{sc.Registry}
		set("registers", fmt.Sprintf("%s on port %d", sc.Register.Name, sc.Register.Port))
	}
	return info
}
