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

	if len(g.Nodes) != 2 || len(g.Nodes[0].Chips) != 1 || g.Nodes[0].Chips[0].Name != "greeter-agent" {
		t.Errorf("the agent is not a chip in greeter's pod: %+v", g.Nodes)
	}

	if got := inbound([]row{svc("consul", "infra"), agent}, svc("consul", "infra")); len(got) != 1 || got[0] != "greeter-agent" {
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

// An app with replicas is one stacked card; its sidecars are chips (a
// transparent Envoy marked); an app-to-app edge loops through the gutter.
func TestLayoutDrawsPodsReplicasAndAppEdges(t *testing.T) {
	inst := func(name string, n int) row {
		r := svc(name, "app")
		r.App, r.Instance, r.Replicas = "web", n, 3

		return r
	}
	envoy := func(parent string, transparent bool) row {
		r := svc(parent+"-envoy", "sidecar")
		r.Parent, r.Type = parent, "envoy"
		r.Sidecar = &core.SidecarInfo{Transparent: transparent, Egress: "deny"}
		r.Health = core.HealthHealthy

		return r
	}
	caller := svc("caller", "app", core.Edge{To: "postgres", Port: 5432, TLS: true}, core.Edge{To: "web", Port: 8443, TLS: true, Via: "envoy", ToApp: true})
	caller.App, caller.Replicas = "caller", 1
	broken := envoy("web-3", false)
	broken.Health = core.HealthUnhealthy
	rows := []row{
		svc("postgres", "infra"),
		inst("web-1", 1), envoy("web-1", false),
		inst("web-2", 2), envoy("web-2", false),
		inst("web-3", 3), broken,
		caller, envoy("caller", true),
	}

	g := layout(rows, "web-2")
	if len(g.Nodes) != 3 {
		t.Fatalf("nodes = %d, want postgres, web, caller", len(g.Nodes))
	}

	web, call := g.Nodes[0], g.Nodes[1]
	if web.Label() != "web" || web.Replicas() != 3 || len(web.Stack()) != 2 || !web.Selected {
		t.Errorf("web card = label %q replicas %d stack %v selected %v", web.Label(), web.Replicas(), web.Stack(), web.Selected)
	}

	if len(web.Chips) != 1 || web.Chips[0].Label != "envoy" || web.Chips[0].Color != "red" || web.Chips[0].Name != "web-1-envoy" {
		t.Errorf("web chips = %+v (want one envoy chip, red because web-3's is unhealthy)", web.Chips)
	}

	if len(call.Chips) != 1 || !call.Chips[0].Transparent || !strings.Contains(call.Chips[0].Label, "transparent") {
		t.Errorf("caller chips = %+v", call.Chips)
	}

	if call.Y <= web.Y+web.H {
		t.Errorf("caller (y %d) overlaps web's pod (y %d, h %d)", call.Y, web.Y, web.H)
	}

	if g.CodeX != graphPad+appGutter {
		t.Errorf("no gutter for the app-to-app edge: code column at %d", g.CodeX)
	}

	var appEdge *edge

	for i := range g.Edges {
		if g.Edges[i].ToApp {
			appEdge = &g.Edges[i]
		}
	}

	if len(g.Edges) != 2 || appEdge == nil || appEdge.Label != "TLS :8443" || appEdge.LabelX >= g.CodeX {
		t.Errorf("edges = %+v", g.Edges)
	}

	if got := selectedNode(dashboardData{Services: rows}, "web"); got != "web-1" {
		t.Errorf("selecting an app with replicas = %q, want its first instance", got)
	}

	if got := inbound(rows, inst("web-2", 2)); len(got) != 1 || got[0] != "caller" {
		t.Errorf("web-2 used by = %v", got)
	}
}
