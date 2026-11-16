package model

import (
	"slices"
	"testing"

	"github.com/brian-nunez/breadboard/internal/spec"
)

func TestResolve(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "db", Type: "postgres", Resources: spec.Resources{CPUs: "2"}}, {Name: "kafka", Type: "kafka"}},
		Apps: []spec.App{
			{Name: "api", Path: "./api", Uses: []spec.Use{{Service: "db"}, {Service: "kafka", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "bench"}}}, Health: &spec.Health{Port: 9000}},
			{Name: "worker", Path: "./w", Uses: []spec.Use{{Service: "kafka", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "bench too"}}}},
		},
	}
	m, err := Resolve(s, "lab.internal")
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "bb-lab" || len(m.Services) != 4 {
		t.Fatalf("model = %+v", m)
	}
	db, _ := m.Service("db")
	if db.FQDN != "db.lab.internal" || db.Identity != "spiffe://lab.internal/experiment/lab/service/db" {
		t.Errorf("db naming = %s %s", db.FQDN, db.Identity)
	}
	if db.Resources.CPUs != "2" || db.Resources.Memory != db.Entry.Resources.Memory {
		t.Errorf("db resources = %+v", db.Resources)
	}
	if db.Config["database"] != "app" || db.Config["user"] != "app" {
		t.Errorf("db config defaults = %v", db.Config)
	}
	api, _ := m.Service("api")
	if api.Health == nil || api.Health.Port != 9000 || api.Health.Path != DefaultAppHealthPath || api.Resources.Memory != DefaultAppMemory {
		t.Errorf("api defaults = %+v %+v", api.Health, api.Resources)
	}
	edges := m.EdgesFrom("api")
	if len(edges) != 2 || !edges[0].TLS || edges[0].Port != 5432 || edges[1].TLS || edges[1].Port != 9092 || edges[1].Reason != "bench" {
		t.Errorf("api edges = %+v", edges)
	}
	if w, _ := m.Service("worker"); w.Health != nil {
		t.Errorf("worker health = %+v, want the default process check", w.Health)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments = %+v", m.Segments)
	}
	seg := m.Segments[0]
	if seg.Name != "plain-kafka" || seg.Target != "kafka" || !slices.Equal(seg.Clients, []string{"api", "worker"}) || len(seg.Reasons) != 2 {
		t.Errorf("segment = %+v", seg)
	}
	if got := m.SegmentsOf("db"); len(got) != 0 {
		t.Errorf("db is on segments %v", got)
	}
	if got := m.SegmentsOf("worker"); len(got) != 1 {
		t.Errorf("worker segments = %v", got)
	}
	if len(m.PlaintextEdges()) != 2 {
		t.Errorf("plaintext edges = %v", m.PlaintextEdges())
	}
	if SegmentFQDN("kafka", "lab.internal") != "kafka.plain.lab.internal" {
		t.Error("SegmentFQDN")
	}
}

func TestResolveRejectsWhatValidationMissed(t *testing.T) {
	for _, s := range []*spec.Spec{
		{Name: "x", Services: []spec.Service{{Name: "a", Type: "nope"}}},
		{Name: "x", Apps: []spec.App{{Name: "a", Uses: []spec.Use{{Service: "ghost"}}}}},
		{Name: "x", Services: []spec.Service{{Name: "r", Type: "redis"}}, Apps: []spec.App{{Name: "a", Uses: []spec.Use{{Service: "r", TLS: spec.TLS{Mode: spec.TLSOff}}}}}},
	} {
		if _, err := Resolve(s, "bb.internal"); err == nil {
			t.Errorf("Resolve(%+v) succeeded", s)
		}
	}
}

func TestRegisterAddsASidecar(t *testing.T) {
	s := &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "registry", Type: "consul"}},
		Apps: []spec.App{
			{Name: "greeter", Path: "./g", Uses: []spec.Use{{Service: "registry"}}, Register: &spec.Register{Port: 8443}},
			{Name: "named", Path: "./n", Uses: []spec.Use{{Service: "registry"}}, Register: &spec.Register{Name: "hello", Port: 9000, Check: "/healthz"}},
		},
	}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	sc, ok := m.Service("greeter-agent")
	if !ok {
		t.Fatalf("no sidecar in %v", m.Services)
	}
	if sc.Kind != Sidecar || sc.Parent != "greeter" || sc.Registry != "registry" || sc.FQDN != "greeter.bb.internal" || sc.Entry.ID != "consul" {
		t.Errorf("sidecar = %+v", sc)
	}
	if *sc.Register != (spec.Register{Name: "greeter", Port: 8443}) {
		t.Errorf("register defaults = %+v", sc.Register)
	}
	if n, _ := m.Service("named-agent"); *n.Register != (spec.Register{Name: "hello", Port: 9000, Check: "/healthz"}) {
		t.Errorf("explicit register = %+v", n.Register)
	}
	if len(m.EdgesFrom("greeter-agent")) != 0 {
		t.Error("a sidecar must not have edges of its own")
	}
	s.Apps[0].Uses = nil
	if _, err := Resolve(s, "bb.internal"); err == nil {
		t.Error("register without a registry in uses resolved")
	}
}
