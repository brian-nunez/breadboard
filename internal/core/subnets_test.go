package core

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

// labBlock is the /24 of segmentPool k blocks after where the harness
// experiment "lab" starts its search (D-202).
func labBlock(k int) netip.Prefix {
	return netip.MustParsePrefix(fmt.Sprintf("10.213.%d.0/24", (subnetStart("lab")+k)%256))
}

// labAddr is host's address in labBlock(k).
func labAddr(k, host int) string {
	b := labBlock(k).Addr().As4()
	b[3] = byte(host)

	return netip.AddrFrom4(b).String()
}

// plaintextLab is a harness whose lab, named name, has one private segment.
func plaintextLab(t *testing.T, name string) *harness {
	t.Helper()
	h := setupStarter(t)
	h.add("postgres")

	spec := h.read("experiment.yaml") + "  pooler:\n    path: ./producer-consumer\n    uses:\n      - service: postgres\n        tls: {mode: off, reason: terminate TLS at a pooler}\n"
	if name != "lab" {
		spec = strings.Replace(spec, "name: lab\n", "name: "+name+"\n", 1)
	}

	h.writeSpec(spec)

	return h
}

func (h *harness) segment() string {
	h.t.Helper()

	ps, err := state.Project{Dir: h.dir}.LoadState()
	if err != nil {
		h.t.Fatal(err)
	}

	return ps.Segments["plain-postgres"]
}

// Two labs starting at once see the same free pool. Their searches start
// at different blocks, so they do not pick the same /24.
func TestLabsStartTheirSubnetSearchApart(t *testing.T) {
	if subnetStart("lab") == subnetStart("other") {
		t.Fatal("test names hash to the same start; pick another name")
	}
	// Each lab sees the same empty pool and takes its own start block.
	for _, name := range []string{"lab", "other"} {
		t.Run(name, func(t *testing.T) {
			h := plaintextLab(t, name)
			if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
				t.Fatal(err)
			}

			if want := fmt.Sprintf("10.213.%d.0/24", subnetStart(name)); h.segment() != want {
				t.Fatalf("segment = %s, want %s", h.segment(), want)
			}
		})
	}
}

// The search wraps past the last block of the pool to the first.
func TestSubnetSearchWraps(t *testing.T) {
	h := plaintextLab(t, "lab")
	for i := subnetStart("lab"); i < 256; i++ {
		h.rt.subnets = append(h.rt.subnets, runtime.Network{Subnet: netip.MustParsePrefix(fmt.Sprintf("10.213.%d.0/24", i)), ExperimentID: "someone-else"})
	}

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if h.segment() != "10.213.0.0/24" {
		t.Fatalf("segment = %s, want the pool's first block after wrapping", h.segment())
	}
}

// Docker refused a chosen subnet, but its listing does not show the
// network that took it yet. The retry must not choose the same subnet.
func TestRetryAvoidsTheSubnetThatFailed(t *testing.T) {
	h := plaintextLab(t, "lab")
	ups := 0

	h.rt.onUp = func() error {
		ups++
		if ups == 1 {
			return fmt.Errorf("%w: docker compose up: exit status 1", runtime.ErrSubnetTaken)
		}

		return nil
	}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if ups != 2 || h.segment() != labBlock(1).String() {
		t.Fatalf("ups = %d, segment = %s; want a second up on %s", ups, h.segment(), labBlock(1))
	}

	// The next bb up starts afresh: the subnet it holds is kept.
	ups = 1

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if h.segment() != labBlock(1).String() {
		t.Fatalf("segment moved to %s", h.segment())
	}
}

// A retry keeps a subnet that this experiment's own network holds:
// compose created it before the failure.
func TestRetryKeepsASubnetItsOwnNetworkHolds(t *testing.T) {
	h := plaintextLab(t, "lab")
	ups := 0

	h.rt.onUp = func() error {
		ups++
		if ups == 1 {
			ps, _ := state.Project{Dir: h.dir}.LoadState()
			h.rt.subnets = []runtime.Network{{Name: "bb-lab_plain-postgres", Subnet: labBlock(0), ExperimentID: ps.ExperimentID}}

			return fmt.Errorf("%w: docker compose up: exit status 1", runtime.ErrSubnetTaken)
		}

		return nil
	}
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if ups != 2 || h.segment() != labBlock(0).String() {
		t.Fatalf("ups = %d, segment = %s; want %s kept", ups, h.segment(), labBlock(0))
	}
}

// A subnet recorded before D-202 (allocated from the pool's first block)
// stays where it is.
func TestStoredSubnetIsKept(t *testing.T) {
	h := plaintextLab(t, "lab")
	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	p := state.Project{Dir: h.dir}

	ps, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}

	ps.Segments["plain-postgres"] = "10.213.0.0/24"
	if err := p.SaveState(ps, "test"); err != nil {
		t.Fatal(err)
	}

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	if h.segment() != "10.213.0.0/24" {
		t.Fatalf("segment = %s, want the stored subnet kept", h.segment())
	}
}
