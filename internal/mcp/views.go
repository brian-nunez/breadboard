package mcp

import (
	"time"

	"github.com/brian-nunez/breadboard/internal/core"
)

// The core's results are Go data without JSON names. These views are how the
// MCP wrapper renders them: snake_case fields, times as RFC 3339 strings and
// file contents as text, so a model reads them directly.

type event struct {
	Kind    string `json:"kind" jsonschema:"step, health, warning, or info"`
	Message string `json:"message,omitempty"`
	Service string `json:"service,omitempty"`
	Health  string `json:"health,omitempty"`
}

func eventView(ev core.Event) event {
	return event{Kind: string(ev.Kind), Message: ev.Message, Service: ev.Service, Health: ev.Health}
}

func timeView(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}

	return out
}

type serviceStatus struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind" jsonschema:"infra, app, or sidecar"`
	Type        string   `json:"type,omitempty" jsonschema:"catalog type, for infra and sidecars"`
	Image       string   `json:"image,omitempty"`
	Address     string   `json:"address,omitempty"`
	State       string   `json:"state" jsonschema:"container state: running, exited, ..., or not created"`
	Health      string   `json:"health"`
	CPUPercent  float64  `json:"cpu_percent"`
	CPULimit    string   `json:"cpu_limit,omitempty"`
	MemoryBytes uint64   `json:"memory_bytes"`
	MemoryLimit string   `json:"memory_limit,omitempty"`
	OOMKilled   bool     `json:"oom_killed"`
	StartedAt   string   `json:"started_at,omitempty"`
	Uses        []edge   `json:"uses"`
	Path        string   `json:"path,omitempty" jsonschema:"apps: source directory"`
	Command     []string `json:"command,omitempty" jsonschema:"apps: command from experiment.yaml"`
	App         string   `json:"app,omitempty" jsonschema:"app instances: the app in experiment.yaml"`
	Instance    int      `json:"instance,omitempty" jsonschema:"app instances: 1..N for a replica, absent when the app runs one"`
	Replicas    int      `json:"replicas,omitempty" jsonschema:"app instances: how many replicas the app runs"`
	Sidecars    []string `json:"sidecars,omitempty" jsonschema:"app instances: its sidecars' service names; their rows follow it"`
	Parent      string   `json:"parent,omitempty" jsonschema:"sidecars: the app instance whose network namespace they share"`
	Registry    string   `json:"registry,omitempty" jsonschema:"registration agents: the registry they join"`
	Sidecar     *sidecar `json:"sidecar,omitempty" jsonschema:"sidecars: what it is, its ports, and the env it gives the app"`
	HostURLs    []string `json:"host_urls,omitempty" jsonschema:"URLs that reach it from the host through the host router: an app instance's app URL, then its own with replicas"`
	HostRouter  bool     `json:"host_router,omitempty" jsonschema:"the lab's host router (bb-host), which serves every host URL"`
}

type edge struct {
	To        string `json:"to"`
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"`
	Reason    string `json:"reason,omitempty" jsonschema:"why TLS is off, when it is"`
	Via       string `json:"via,omitempty" jsonschema:"the Envoy sidecar whose outbound listener carries the edge"`
	ToApp     bool   `json:"to_app,omitempty" jsonschema:"the target is an app (every replica)"`
	Mode      string `json:"mode,omitempty" jsonschema:"a proxy's upstream edge: on (re-encrypt), off (terminate, plaintext on a private segment) or passthrough"`
	Discovery string `json:"discovery,omitempty" jsonschema:"a proxy's discovery edge: registry (it discovers routes in this Consul service, config consul_catalog) or route (it routes to this app, registered there with the tag traefik.enable=true)"`
}

type sidecarPort struct {
	Port     int    `json:"port"`
	Loopback bool   `json:"loopback" jsonschema:"bound on 127.0.0.1: only the app and its sidecars reach it, in plaintext"`
	Purpose  string `json:"purpose"`
}

type sidecarSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sidecar struct {
	Name        string           `json:"name" jsonschema:"name within the app"`
	Service     string           `json:"service" jsonschema:"the container's service name, for logs"`
	Type        string           `json:"type" jsonschema:"envoy, pgbouncer, fluent-bit, or consul-agent"`
	Title       string           `json:"title"`
	Summary     string           `json:"summary"`
	Image       string           `json:"image,omitempty"`
	Ports       []sidecarPort    `json:"ports"`
	Env         []envVar         `json:"env" jsonschema:"variables it adds to the app's environment; secret values are empty"`
	Rewrites    []envVar         `json:"rewrites" jsonschema:"app variables it points at itself, with their new values; secret values are empty"`
	Settings    []sidecarSetting `json:"settings"`
	Transparent bool             `json:"transparent,omitempty" jsonschema:"Envoy captures all of the app's traffic"`
	Egress      string           `json:"egress,omitempty" jsonschema:"transparent Envoy: allow or deny undeclared destinations"`
	Targets     []string         `json:"targets,omitempty"`
	Registry    string           `json:"registry,omitempty"`
}

func sidecarView(s core.SidecarInfo) sidecar {
	return sidecar{
		Name: s.Name, Service: s.Service, Type: s.Type, Title: s.Title, Summary: s.Summary, Image: s.Image,
		Ports: mapSlice(s.Ports, func(p core.SidecarPort) sidecarPort {
			return sidecarPort{Port: p.Port, Loopback: p.Loopback, Purpose: p.Purpose}
		}),
		Env: mapSlice(s.Env, envVarView), Rewrites: mapSlice(s.Rewrites, envVarView),
		Settings:    mapSlice(s.Settings, func(v core.SidecarSetting) sidecarSetting { return sidecarSetting{Name: v.Name, Value: v.Value} }),
		Transparent: s.Transparent, Egress: s.Egress, Targets: s.Targets, Registry: s.Registry,
	}
}

func sidecarPtr(s *core.SidecarInfo) *sidecar {
	if s == nil {
		return nil
	}

	v := sidecarView(*s)

	return &v
}

func statusView(s core.ServiceStatus) serviceStatus {
	return serviceStatus{
		Name: s.Name, Kind: s.Kind, Type: s.Type, Image: s.Image, Address: s.Address,
		State: s.State, Health: s.Health, CPUPercent: s.CPUPercent, CPULimit: s.CPULimit,
		MemoryBytes: s.MemoryBytes, MemoryLimit: s.MemoryLimit, OOMKilled: s.OOMKilled,
		StartedAt: timeView(s.StartedAt), Uses: mapSlice(s.Uses, edgeView),
		Path: s.Path, Command: s.Command,
		App: s.App, Instance: s.Instance, Replicas: s.Replicas, Sidecars: s.Sidecars,
		Parent: s.Parent, Registry: s.Registry, Sidecar: sidecarPtr(s.Sidecar),
		HostURLs: s.HostURLs, HostRouter: s.HostRouter,
	}
}

type hostURL struct {
	URL      string `json:"url"`
	Target   string `json:"target" jsonschema:"the app or service in experiment.yaml"`
	Instance string `json:"instance,omitempty" jsonschema:"the replica this URL reaches alone (api-2); absent for a URL that reaches every instance"`
}

func hostURLView(u core.HostURL) hostURL {
	return hostURL{URL: u.URL, Target: u.Target, Instance: u.Instance}
}

func edgeView(e core.Edge) edge {
	return edge{To: e.To, Port: e.Port, TLS: e.TLS, Reason: e.Reason, Via: e.Via, ToApp: e.ToApp, Mode: e.Mode, Discovery: e.Discovery}
}

type envVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

func envVarView(v core.EnvVar) envVar { return envVar{Name: v.Name, Value: v.Value, Secret: v.Secret} }
