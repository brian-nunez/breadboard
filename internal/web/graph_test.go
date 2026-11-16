package web

import (
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
)

func svc(name, kind string, uses ...core.Edge) row {
	return row{ServiceStatus: core.ServiceStatus{Name: name, Kind: kind, Uses: uses}, TLS: true}
}

func TestLayoutPutsCodeLeftAndInfraNearItsUsers(t *testing.T) {
	rows := []row{
		svc("redis", "infra"),
		svc("kafka", "infra"),
		svc("postgres", "infra"),
		svc("producer", "app", core.Edge{To: "kafka", Port: 9093, TLS: true}),
		svc("worker", "app", core.Edge{To: "postgres", Port: 5432, Reason: "pooler"}),
	}
	g := layout(rows, "worker")
	at := map[string]node{}
	for _, n := range g.Nodes {
		at[n.Name] = n
	}
	for _, name := range []string{"producer", "worker"} {
		if at[name].X != g.CodeX {
			t.Errorf("%s is not in the code column", name)
		}
	}
	for _, name := range []string{"kafka", "postgres", "redis"} {
		if at[name].X != g.InfraX {
			t.Errorf("%s is not in the infrastructure column", name)
		}
	}
	// Infra sits level with (or below) its users; unused infra goes last.
	if at["kafka"].Y != at["producer"].Y || at["postgres"].Y < at["worker"].Y || at["redis"].Y <= at["postgres"].Y {
		t.Errorf("rows: producer %d kafka %d worker %d postgres %d redis %d", at["producer"].Y, at["kafka"].Y, at["worker"].Y, at["postgres"].Y, at["redis"].Y)
	}
	if !at["worker"].Selected || at["producer"].Selected {
		t.Error("selection not applied")
	}
	if len(g.Edges) != 2 {
		t.Fatalf("edges = %d", len(g.Edges))
	}
	plain := g.Edges[1]
	if !plain.Plaintext || plain.Label != "plaintext :5432" || plain.Reason != "pooler" || !strings.HasPrefix(plain.Path, "M") {
		t.Errorf("plaintext edge = %+v", plain)
	}
	if g.Edges[0].Label != "TLS :9093" || g.Edges[0].Plaintext {
		t.Errorf("TLS edge = %+v", g.Edges[0])
	}
}

func TestSidecarsRegisterWithTheirRegistry(t *testing.T) {
	agent := svc("greeter-agent", "sidecar")
	agent.Parent, agent.Registry = "greeter", "consul"
	g := layout([]row{svc("consul", "infra"), svc("greeter", "app"), agent}, "")
	if len(g.Edges) != 1 || !g.Edges[0].Registering || g.Edges[0].Label != "registers" {
		t.Fatalf("edges = %+v", g.Edges)
	}
	if got := inbound([]row{svc("consul", "infra"), agent}, "consul"); len(got) != 1 || got[0] != "greeter-agent" {
		t.Errorf("inbound = %v", got)
	}
}

func TestSelectedNodeFallsBackToTheFirstApp(t *testing.T) {
	d := dashboardData{Services: []row{svc("kafka", "infra"), svc("producer", "app")}}
	for want, got := range map[string]string{"kafka": "kafka", "nope": "producer", "": "producer"} {
		if s := selectedNode(d, want); s != got {
			t.Errorf("selectedNode(%q) = %q, want %q", want, s, got)
		}
	}
}
