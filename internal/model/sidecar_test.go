package model

import (
	"slices"
	"testing"

	"github.com/brian-nunez/breadboard/internal/spec"
)

func names(svcs []*Service) []string {
	out := make([]string, len(svcs))
	for i, s := range svcs {
		out[i] = s.Name
	}
	return out
}

func TestReplicasAreNamedInstances(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "consul", Type: "consul"}},
		Apps:     []spec.App{{Name: "api", Path: "./api", Replicas: 3, Uses: []spec.Use{{Service: "consul"}}, Register: &spec.Register{Port: 8443}}},
	}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"consul", "api-1", "api-1-agent", "api-2", "api-2-agent", "api-3", "api-3-agent"}
	if got := names(m.Services); !slices.Equal(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	insts := m.Instances("api")
	if len(insts) != 3 {
		t.Fatalf("instances = %v", names(insts))
	}
	for i, inst := range insts {
		if inst.Instance != i+1 || inst.Group() != "api" || inst.Pod != "" || inst.SharedFQDN != "api.bb.internal" {
			t.Errorf("instance %d = %+v", i+1, inst)
		}
		if !slices.Equal(inst.Aliases, []string{inst.Name + ".bb.internal", "api.bb.internal"}) {
			t.Errorf("%s aliases = %v", inst.Name, inst.Aliases)
		}
		if inst.Identity != insts[0].Identity {
			t.Error("replicas must share one identity")
		}
	}
	agent, _ := m.Service("api-2-agent")
	if agent.Parent != "api-2" || agent.FQDN != "api-2.bb.internal" || agent.Register.Name != "api" || agent.Image != "hashicorp/consul:1.22.7" {
		t.Errorf("agent = %+v", agent)
	}
	// Edges belong to the app, once, not to each instance.
	if e := m.EdgesFrom("api"); len(e) != 1 || e[0].To != "consul" {
		t.Errorf("edges = %+v", m.Edges)
	}
}

func TestOneReplicaKeepsTheAppsName(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab", Apps: []spec.App{{Name: "api", Path: "./api", Replicas: 1}}}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	api, ok := m.Service("api")
	if !ok || api.Instance != 0 || !slices.Equal(api.Aliases, []string{"api.bb.internal"}) || api.Pod != "" {
		t.Errorf("api = %+v", api)
	}
}

func TestSidecarsMakeAPod(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "consul", Type: "consul"}, {Name: "s3", Type: "s3-compatible"}},
		Apps: []spec.App{
			{Name: "web", Path: "./web", Uses: []spec.Use{{Service: "consul"}}, Register: &spec.Register{Port: 9443},
				Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
					Outbound:    []spec.EnvoyOutbound{{Service: "api", Port: 9001}, {Service: "s3", Port: 9002}},
					Transparent: &spec.Transparent{},
				}}}},
			{Name: "api", Path: "./api", Replicas: 2,
				Sidecars: []spec.Sidecar{{Type: "envoy", Name: "edge", Envoy: &spec.EnvoySidecar{Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080}}}}},
		},
	}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"consul", "s3", "web", "web-envoy", "web-agent", "api-1", "api-1-edge", "api-2", "api-2-edge"}
	if got := names(m.Services); !slices.Equal(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	web, _ := m.Service("web")
	if web.Pod != "web-pod" || web.Netinit != "web-netinit" || !slices.Equal(web.Sidecars, []string{"web-envoy", "web-agent"}) {
		t.Errorf("web = %+v", web)
	}
	api2, _ := m.Service("api-2")
	if api2.Pod != "api-2-pod" || api2.Netinit != "" || !slices.Equal(api2.Sidecars, []string{"api-2-edge"}) {
		t.Errorf("api-2 = %+v", api2)
	}
	envoy, _ := m.Service("web-envoy")
	if envoy.Kind != Sidecar || envoy.Parent != "web" || envoy.SidecarType.ID != "envoy" || envoy.Image != "envoyproxy/envoy:v1.40.2" || envoy.Sidecar.Name != "envoy" {
		t.Errorf("envoy = %+v", envoy)
	}
	// Defaults applied in the model.
	if o := envoy.Sidecar.Envoy.Outbound[0]; o.LB != "round_robin" || *o.Retries != 2 || envoy.Sidecar.Envoy.Transparent.Egress != "allow" {
		t.Errorf("outbound defaults = %+v", o)
	}
	// Targets: the app by its shared name and TLS port (its envoy inbound), infra by its listener.
	if len(envoy.Targets) != 2 {
		t.Fatalf("targets = %+v", envoy.Targets)
	}
	if tg := envoy.Targets[0]; !tg.App || tg.FQDN != "api.bb.internal" || tg.Port != 8443 {
		t.Errorf("app target = %+v", tg)
	}
	if tg := envoy.Targets[1]; tg.App || tg.FQDN != "s3.bb.internal" || tg.Port != 8333 {
		t.Errorf("infra target = %+v", tg)
	}
	var viaEdges []Edge
	for _, e := range m.EdgesFrom("web") {
		if e.Via != "" {
			viaEdges = append(viaEdges, e)
		}
	}
	if len(viaEdges) != 2 || viaEdges[0] != (Edge{From: "web", To: "api", Listener: "tls", Port: 8443, TLS: true, Via: "envoy", ToApp: true}) || viaEdges[1].To != "s3" || viaEdges[1].ToApp {
		t.Errorf("outbound edges = %+v", viaEdges)
	}
	agent, _ := m.Service("web-agent")
	if agent.Parent != "web" || agent.SidecarType.ID != "consul-agent" {
		t.Errorf("agent in a pod = %+v", agent)
	}
	if len(m.Segments) != 0 {
		t.Errorf("TLS edges made segments: %+v", m.Segments)
	}
}

func TestRegisterOnlyStaysOutOfAPod(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "consul", Type: "consul"}},
		Apps:     []spec.App{{Name: "g", Path: "./g", Uses: []spec.Use{{Service: "consul"}}, Register: &spec.Register{Port: 8443}}},
	}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := m.Service("g")
	if g.Pod != "" || g.Netinit != "" || !slices.Equal(g.Sidecars, []string{"g-agent"}) {
		t.Errorf("g = %+v", g)
	}
}
