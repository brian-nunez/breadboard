package sidecar

import (
	"regexp"
	"testing"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// Every type a spec can declare has a catalog entry, and every image is an
// exact pin.
func TestEveryDeclarableTypeHasAnEntry(t *testing.T) {
	pinned := regexp.MustCompile(`^[a-z0-9./-]+:v?[0-9]+\.[0-9]+\.[0-9]+[a-z0-9.-]*$`)
	for _, id := range spec.SidecarTypes {
		typ, ok := Lookup(id)
		if !ok || typ.Internal {
			t.Errorf("%s: entry = %+v %v", id, typ, ok)
			continue
		}
		if !pinned.MatchString(typ.Image) {
			t.Errorf("%s image %q is not an exact pin", id, typ.Image)
		}
		if typ.Resources.Memory == "" || typ.Resources.CPUs == "" {
			t.Errorf("%s has no resource defaults", id)
		}
	}
	if !pinned.MatchString(PodImage) {
		t.Errorf("pod image %q is not an exact pin", PodImage)
	}
	if typ, ok := Lookup(spec.SidecarConsulAgent); !ok || !typ.Internal || typ.Render == nil {
		t.Errorf("consul-agent = %+v %v", typ, ok)
	}
}

func TestUnbuiltTypesFailClearly(t *testing.T) {
	for _, typ := range All() {
		if typ.Render != nil {
			continue
		}
		if _, err := typ.RenderSidecar(Input{Name: "api-" + typ.ID}); err == nil {
			t.Errorf("%s rendered without a renderer", typ.ID)
		}
	}
}

// The registration agent is rendered by its registry's catalog entry.
func TestConsulAgentDelegatesToTheRegistry(t *testing.T) {
	consul, _ := catalog.Lookup("consul")
	typ, _ := Lookup(spec.SidecarConsulAgent)
	out, err := typ.RenderSidecar(Input{
		Name:     "api-2-agent",
		App:      App{Service: "api-2", Name: "api", FQDN: "api-2.bb.internal", SharedFQDN: "api.bb.internal", Instance: 2},
		Secret:   func(string) func(string) string { return func(k string) string { return "S" } },
		Register: &Register{Entry: consul, RegistryService: "consul", RegistryFQDN: "consul.bb.internal", Service: "api", Port: 8443},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Needs) != 1 || out.Needs[0] != "consul" || len(out.Health.Test) == 0 || len(out.Files) != 1 {
		t.Errorf("output = %+v", out)
	}
	if _, err := typ.RenderSidecar(Input{Name: "x"}); err == nil {
		t.Error("consul-agent rendered without a registry")
	}
}
