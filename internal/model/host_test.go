package model

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/errs"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// hostLab has every kind of host target: an app with 3 replicas (port from
// host.port), an app with one instance (port from its Envoy inbound), and
// an HAProxy proxy (its HTTPS listener).
func hostLab() *spec.Spec {
	return &spec.Spec{Version: 1, Name: "shop",
		Services: []spec.Service{
			{Name: "db", Type: "postgres"},
			{Name: "edge", Type: "haproxy", Host: &spec.Host{}, Upstreams: []spec.Upstream{{Service: "api", Port: 8443}}},
		},
		Apps: []spec.App{
			{Name: "api", Path: "./api", Replicas: 3, Host: &spec.Host{Port: 8443}},
			{Name: "web", Path: "./web", Host: &spec.Host{}, Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
				Inbound: &spec.EnvoyInbound{Port: 9443, AppPort: 8080},
			}}}},
			{Name: "worker", Path: "./worker"},
		},
	}
}

func TestHostTargetsResolve(t *testing.T) {
	s := hostLab()
	if err := spec.Validate(s, catalog.Types); err != nil {
		t.Fatal(err)
	}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	router, ok := m.HostRouter()
	if !ok {
		t.Fatal("no host router")
	}
	if m.Services[len(m.Services)-1] != router {
		t.Error("the host router is not the last service")
	}
	if router.Name != spec.HostRouterName || router.Kind != Infra || router.Entry.ID != "traefik" || router.FQDN != "bb-host.bb.internal" ||
		!reflect.DeepEqual(router.Aliases, []string{"bb-host.bb.internal"}) || router.Identity != "spiffe://bb.internal/experiment/shop/service/bb-host" ||
		router.Resources != (catalog.Resources{Memory: "128m", CPUs: "0.25"}) || router.Image != router.Entry.Image {
		t.Errorf("router = %+v", router)
	}
	if len(router.Upstreams) != 0 || router.File != "" {
		t.Errorf("router has upstreams %v or a file %q", router.Upstreams, router.File)
	}
	if len(m.EdgesFrom(router.Name)) != 0 || len(m.SegmentsOf(router.Name)) != 0 {
		t.Error("the router has edges or segments")
	}
	want := []catalog.HostTarget{
		{Target: "edge", Name: "edge.shop.localhost", ServerName: "edge.bb.internal", Port: 8443,
			Instances: []catalog.HostInstance{{Service: "edge", FQDN: "edge.bb.internal"}}},
		{Target: "api", App: true, Name: "api.shop.localhost", ServerName: "api.bb.internal", Port: 8443, Instances: []catalog.HostInstance{
			{Service: "api-1", FQDN: "api-1.bb.internal", Name: "api-1.shop.localhost"},
			{Service: "api-2", FQDN: "api-2.bb.internal", Name: "api-2.shop.localhost"},
			{Service: "api-3", FQDN: "api-3.bb.internal", Name: "api-3.shop.localhost"},
		}},
		{Target: "web", App: true, Name: "web.shop.localhost", ServerName: "web.bb.internal", Port: 9443,
			Instances: []catalog.HostInstance{{Service: "web", FQDN: "web.bb.internal"}}},
	}
	if !reflect.DeepEqual(router.HostTargets, want) {
		t.Errorf("targets =\n%+v\nwant\n%+v", router.HostTargets, want)
	}
	names := catalog.HostNames(router.HostTargets)
	wantNames := []string{"edge.shop.localhost", "api.shop.localhost", "api-1.shop.localhost", "api-2.shop.localhost", "api-3.shop.localhost", "web.shop.localhost"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("names = %v, want %v", names, wantNames)
	}
}

func TestNoHostRouterWithoutHost(t *testing.T) {
	s := hostLab()
	s.Services[1].Host = nil
	for i := range s.Apps {
		s.Apps[i].Host = nil
	}
	m, err := Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := m.HostRouter(); ok {
		t.Errorf("router %s without a host target", r.Name)
	}
	if _, ok := m.Service(spec.HostRouterName); ok {
		t.Error("bb-host is in the model")
	}
}

// Host names and lab names overlap when the lab domain is
// <experiment>.localhost or below it; Resolve refuses host access then,
// and only then.
func TestHostRefusesAnOverlappingDomain(t *testing.T) {
	for _, domain := range []string{"shop.localhost", "lab.shop.localhost", "a.b.shop.localhost"} {
		_, err := Resolve(hostLab(), domain)
		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "shop.localhost") {
			t.Errorf("domain %s: err = %v, want a validation error naming shop.localhost", domain, err)
		}
	}
	for _, domain := range []string{"localhost", "other.localhost", "myshop.localhost", "shop.localhost.internal", "bb.internal"} {
		if _, err := Resolve(hostLab(), domain); err != nil {
			t.Errorf("domain %s: %v", domain, err)
		}
	}
	s := hostLab()
	s.Services[1].Host = nil
	for i := range s.Apps {
		s.Apps[i].Host = nil
	}
	if _, err := Resolve(s, "shop.localhost"); err != nil {
		t.Errorf("a lab without host on shop.localhost: %v", err)
	}
}
