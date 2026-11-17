package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brian-nunez/breadboard/internal/errs"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{
		"712MiB":    712 << 20,
		" 1GiB":     1 << 30,
		"1.5KiB":    1536,
		"100B":      100,
		"2MB":       2_000_000,
		"0B":        0,
		"garbage":   0,
		"12.5 MiB ": 13107200,
	} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestLogPrefix(t *testing.T) {
	for in, want := range map[string][2]string{
		"kafka-1  | started":         {"kafka", "started"},
		"consumer  | consumed key=a": {"consumer", "consumed key=a"},
		"my-app-2 | x | y":           {"my-app", "x | y"},
	} {
		m := logPrefix.FindStringSubmatch(in)
		if m == nil || m[1] != want[0] || m[3] != want[1] {
			t.Errorf("%q parsed as %q", in, m)
		}
	}
}

func TestMissingDockerIsUnavailable(t *testing.T) {
	c := &Compose{Bin: "bb-test-no-such-docker"}
	if err := c.Available(context.Background()); !errors.Is(err, errs.ErrDockerUnavailable) {
		t.Fatalf("Available error = %v", err)
	}
	for _, err := range c.Logs(context.Background(), Project{Name: "x"}, nil, false, 0) {
		if !errors.Is(err, errs.ErrDockerUnavailable) {
			t.Fatalf("Logs error = %v", err)
		}
	}
}

func TestUpReportsATakenSubnet(t *testing.T) {
	c := &Compose{}
	for _, msg := range []string{
		"failed to create network bb-lab_default: Error response from daemon: invalid pool request: Pool overlaps with other one on this address space",
		"failed to create network bb-lab_plain-api: Error response from daemon: cannot create network ed30 (br-ed30): conflicts with network b1ab (br-b1ab): networks have overlapping IPv4",
	} {
		bin := filepath.Join(t.TempDir(), "docker")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '"+msg+"' >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c.Bin = bin
		if err := c.Up(context.Background(), Project{Name: "bb-lab"}); !errors.Is(err, ErrSubnetTaken) {
			t.Fatalf("Up error = %v, want ErrSubnetTaken", err)
		}
	}

	other := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho 'no such image' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.Bin = other
	if err := c.Up(context.Background(), Project{Name: "bb-lab"}); err == nil || errors.Is(err, ErrSubnetTaken) {
		t.Fatalf("Up error = %v, want a plain failure", err)
	}
}

// TestSubnetsSkipsANetworkRemovedMidway: another lab's down can remove a
// network between network ls and network inspect; the networks that remain
// are still listed, and any other inspect failure is still an error.
func TestSubnetsSkipsANetworkRemovedMidway(t *testing.T) {
	inspect := `[{"Name":"bb-a_default","Labels":{"breadboard.experiment-id":"a"},"IPAM":{"Config":[{"Subnet":"10.213.0.0/24"}]}}]`
	fake := func(stderr string) string {
		bin := filepath.Join(t.TempDir(), "docker")
		script := "#!/bin/sh\nif [ \"$2\" = ls ]; then echo n1; echo n2; exit 0; fi\necho '" + inspect + "'\necho '" + stderr + "' >&2\nexit 1\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	c := &Compose{Bin: fake("Error response from daemon: network n2 not found")}
	nets, err := c.Subnets(context.Background())
	if err != nil || len(nets) != 1 || nets[0].Subnet.String() != "10.213.0.0/24" || nets[0].ExperimentID != "a" {
		t.Fatalf("Subnets = %+v, %v; want the remaining network", nets, err)
	}
	c.Bin = fake("Cannot connect to the Docker daemon")
	if _, err := c.Subnets(context.Background()); err == nil {
		t.Fatal("Subnets hid a failure that is not a missing network")
	}
}

func TestProjectArgs(t *testing.T) {
	p := Project{Name: "bb-lab", Dir: "/p/.bb/runtime", Files: []string{"/a.yml", "/b.yml"}}
	got := p.args()
	want := []string{"compose", "--project-name", "bb-lab", "--project-directory", "/p/.bb/runtime", "--file", "/a.yml", "--file", "/b.yml"}
	if len(got) != len(want) {
		t.Fatalf("args = %v", got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("args = %v", got)
		}
	}
}
