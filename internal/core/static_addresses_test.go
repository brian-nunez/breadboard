package core

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

// A lab with a transparent pod gets a default network subnet and a fixed
// address per container, recorded in state and stable across ups; a lab
// without one keeps Docker's dynamic addressing.
func TestTransparentLabGetsStableAddresses(t *testing.T) {
	h := visibilityHarness(t)
	if err := os.MkdirAll(filepath.Join(h.dir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "probe", "Dockerfile"), []byte("FROM scratch AS dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.rt.subnets = []runtime.Network{{Subnet: labBlock(0), ExperimentID: "someone-else"}}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	ps, _ := state.Project{Dir: h.dir}.LoadState()

	want := map[string]string{"postgres": labAddr(10), "web-1-pod": labAddr(11), "web-2-pod": labAddr(12), "web-3-pod": labAddr(13), "caller-pod": labAddr(14)}
	if ps.Network != labBlock(1).String() || len(ps.Addresses) != len(want) {
		t.Fatalf("network = %s, addresses = %v", ps.Network, ps.Addresses)
	}

	for k, v := range want {
		if ps.Addresses[k] != v {
			t.Errorf("%s = %s, want %s", k, ps.Addresses[k], v)
		}
	}

	compose := h.read(".bb/runtime/docker-compose.yml")
	for _, s := range []string{"subnet: " + labAddr(0) + "/24", "ip_range: " + labAddr(128) + "/25", "ipv4_address: " + labAddr(14), "address_prefix: " + labAddr(11)} {
		if !strings.Contains(compose+h.read(".bb/runtime/config/caller-envoy/envoy.yaml"), s) {
			t.Errorf("rendered files lack %q", s)
		}
	}
	// A service added before the others takes a new address; nobody moves.
	h.writeSpec(strings.Replace(visibilitySpec, "services:\n", "services:\n  cache:\n    type: redis\n", 1))

	h.rt.subnets = append(h.rt.subnets, runtime.Network{Subnet: labBlock(1), ExperimentID: ps.ExperimentID})
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	ps, _ = state.Project{Dir: h.dir}.LoadState()
	for k, v := range want {
		if ps.Addresses[k] != v {
			t.Errorf("after adding a service, %s moved to %s", k, ps.Addresses[k])
		}
	}

	if ps.Network != labBlock(1).String() || ps.Addresses["cache"] != labAddr(15) {
		t.Errorf("network = %s, cache = %s", ps.Network, ps.Addresses["cache"])
	}
	// Without a transparent pod: no fixed subnet or addresses.
	h.writeSpec(strings.Replace(visibilitySpec, "        transparent: {egress: deny}\n", "", 1))

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	ps, _ = state.Project{Dir: h.dir}.LoadState()
	if ps.Network != "" || ps.Addresses != nil {
		t.Errorf("network = %s, addresses = %v", ps.Network, ps.Addresses)
	}

	if c := h.read(".bb/runtime/docker-compose.yml"); strings.Contains(c, "ipv4_address") || strings.Contains(c, "ipam") {
		t.Error("a lab without a transparent pod got static addressing")
	}
}

// With its pod holder missing, an app's row reports the holder's absence
// in both State and Health, though the app container exists.
func TestStatusWithAMissingPodHolder(t *testing.T) {
	h := visibilityHarness(t)
	h.rt.containers["lab"] = []runtime.Container{
		{ID: "a", Service: "caller", State: "running", Health: "healthy"},
		{ID: "b", Service: "caller-envoy", State: "running", Health: "healthy"},
	}

	res, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}

	c := rowsByName(res.Services)["caller"]
	if c.State != StateNotCreated || c.Health != StateNotCreated {
		t.Errorf("caller with no pod holder = %s/%s, want %s/%s", c.State, c.Health, StateNotCreated, StateNotCreated)
	}
}

// Switching static addressing on (or off) under a running lab stops its
// containers first: Docker cannot change the subnet of a network with
// containers attached. Data volumes stay.
func TestAddressingChangeRecreatesTheNetwork(t *testing.T) {
	h := visibilityHarness(t)
	if err := os.MkdirAll(filepath.Join(h.dir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "probe", "Dockerfile"), []byte("FROM scratch AS dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ps, _ := state.Project{Dir: h.dir}.LoadState()
	h.rt.subnets = []runtime.Network{{Name: "bb-lab_default", Subnet: netip.MustParsePrefix("172.30.0.0/16"), ExperimentID: ps.ExperimentID}}

	h.rt.containers["lab"] = []runtime.Container{{ID: "a", Service: "postgres", State: "running", Health: "healthy"}}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if i, j := slices.Index(h.rt.calls, "down"), slices.Index(h.rt.calls, "up"); i < 0 || j < i {
		t.Errorf("calls = %v, want down (keeping volumes) before up", h.rt.calls)
	}
	// Unchanged on the next up: no down.
	ps, _ = state.Project{Dir: h.dir}.LoadState()
	h.rt.subnets = []runtime.Network{{Name: "bb-lab_default", Subnet: netip.MustParsePrefix(ps.Network), ExperimentID: ps.ExperimentID}}

	h.rt.calls = nil
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if slices.Contains(h.rt.calls, "down") {
		t.Errorf("calls = %v, want no down when the subnet is unchanged", h.rt.calls)
	}
}

// A transparent app whose image starts as a reserved user, or as a name
// that cannot be resolved, is refused before any container starts.
func TestTransparentAppMustNotStartAsAReservedUser(t *testing.T) {
	for name, c := range map[string]struct {
		user runtime.ImageUser
		want string
	}{
		"envoy uid":    {runtime.ImageUser{User: "1337", UID: 1337}, "starts as uid 1337 (USER \"1337\"), which is reserved for Envoy"},
		"agent name":   {runtime.ImageUser{User: "mesh", UID: 1338}, "starts as uid 1338 (USER \"mesh\"), which is reserved for the registration agent"},
		"unresolvable": {runtime.ImageUser{User: "unresolvable"}, "cannot be resolved to a uid"},
	} {
		t.Run(name, func(t *testing.T) {
			h := visibilityHarness(t)
			if err := os.MkdirAll(filepath.Join(h.dir, "probe"), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(h.dir, "probe", "Dockerfile"), []byte("FROM scratch AS dev\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			h.rt.imageUser = map[string]runtime.ImageUser{"caller": c.user}

			_, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "app caller") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Up = %v", err)
			}

			if slices.Contains(h.rt.calls, "up") || !slices.Contains(h.rt.calls, "build caller") {
				t.Errorf("calls = %v, want a build of the transparent app and no up", h.rt.calls)
			}
		})
	}
	// An ordinary user passes, and only transparent apps are checked.
	h := visibilityHarness(t)
	if err := os.MkdirAll(filepath.Join(h.dir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "probe", "Dockerfile"), []byte("FROM scratch AS dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.rt.imageUser = map[string]runtime.ImageUser{"caller": {User: "app", UID: 1000}, "web-1": {User: "1337", UID: 1337}}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
}

// A refused up on a running lab publishes nothing: the active runtime
// files stay byte for byte, and nothing is stopped or started.
func TestRefusedUpKeepsTheActiveBundle(t *testing.T) {
	h := visibilityHarness(t)
	if err := os.MkdirAll(filepath.Join(h.dir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "probe", "Dockerfile"), []byte("FROM scratch AS dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	snapshot := func() map[string]string {
		out := map[string]string{}
		root := filepath.Join(h.dir, ".bb", "runtime")

		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}

			data, err := os.ReadFile(path)
			out[path] = string(data)

			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		return out
	}
	before := snapshot()
	// A config change for a running sidecar, and the transparent app now
	// starts as Envoy's reserved user.
	h.writeSpec(strings.Replace(visibilitySpec, "        service: postgres\n", "        service: postgres\n        pool_size: 20\n", 1))
	h.rt.imageUser = map[string]runtime.ImageUser{"caller": {User: "1337", UID: 1337}}

	h.rt.calls = nil
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Up = %v, want a refusal", err)
	}

	after := snapshot()
	if len(after) != len(before) {
		t.Errorf("runtime files: %d before, %d after", len(before), len(after))
	}

	for path, data := range before {
		if after[path] != data {
			t.Errorf("%s changed on a refused up", path)
		}
	}

	if slices.Contains(h.rt.calls, "up") || slices.Contains(h.rt.calls, "down") {
		t.Errorf("calls = %v, want no down or up", h.rt.calls)
	}

	if left, _ := filepath.Glob(filepath.Join(h.dir, ".bb", "candidate-*")); len(left) > 0 {
		t.Errorf("candidate bundle left behind: %v", left)
	}
}
