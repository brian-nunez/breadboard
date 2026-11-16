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
	Parent      string   `json:"parent,omitempty" jsonschema:"sidecars: the app they register"`
	Registry    string   `json:"registry,omitempty" jsonschema:"sidecars: the registry they join"`
}

type edge struct {
	To     string `json:"to"`
	Port   int    `json:"port"`
	TLS    bool   `json:"tls"`
	Reason string `json:"reason,omitempty" jsonschema:"why TLS is off, when it is"`
}

func statusView(s core.ServiceStatus) serviceStatus {
	return serviceStatus{
		Name: s.Name, Kind: s.Kind, Type: s.Type, Image: s.Image, Address: s.Address,
		State: s.State, Health: s.Health, CPUPercent: s.CPUPercent, CPULimit: s.CPULimit,
		MemoryBytes: s.MemoryBytes, MemoryLimit: s.MemoryLimit, OOMKilled: s.OOMKilled,
		StartedAt: timeView(s.StartedAt), Uses: mapSlice(s.Uses, edgeView),
		Path: s.Path, Command: s.Command, Parent: s.Parent, Registry: s.Registry,
	}
}

func edgeView(e core.Edge) edge {
	return edge{To: e.To, Port: e.Port, TLS: e.TLS, Reason: e.Reason}
}

type envVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

func envVarView(v core.EnvVar) envVar { return envVar{Name: v.Name, Value: v.Value, Secret: v.Secret} }
