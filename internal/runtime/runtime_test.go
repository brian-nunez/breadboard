package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestParsePublishedPorts(t *testing.T) {
	data := []byte(`[
  {"Config": {"Labels": {"breadboard.experiment-id": "abc", "breadboard.service": "bb-host"}}, "State": {"Running": true},
   "HostConfig": {"PortBindings": {"8443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "44312"}]}}},
  {"Config": {"Labels": null}, "State": {"Running": false},
   "HostConfig": {"PortBindings": {"80/tcp": [{"HostIp": "", "HostPort": "44400-44401"}], "53/udp": [{"HostIp": "", "HostPort": ""}]}}},
  {"Config": {"Labels": {}}, "HostConfig": {"PortBindings": null}}
]`)

	got, err := parsePublishedPorts(data)
	if err != nil {
		t.Fatal(err)
	}

	want := map[int]PublishedPort{
		44312: {HostIP: "127.0.0.1", HostPort: 44312, ExperimentID: "abc", Service: "bb-host", Running: true},
		44400: {HostPort: 44400},
		44401: {HostPort: 44401},
	}
	if len(got) != len(want) {
		t.Fatalf("ports = %+v", got)
	}

	for _, p := range got {
		if want[p.HostPort] != p {
			t.Errorf("port %d = %+v, want %+v", p.HostPort, p, want[p.HostPort])
		}
	}
}

func TestOnlyMissingContainers(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Error: No such object: 0123abc\n":                         true,
		"Error response from daemon: No such container: 0123abc\n": true,
		"Error: No such object: a\nError: No such container: b":    true,
		"":                               false,
		"permission denied":              false,
		"Error: No such object: a\nboom": false,
	} {
		if got := onlyMissingContainers(stderr); got != want {
			t.Errorf("%q: %v, want %v", stderr, got, want)
		}
	}
}

// Streaming docker stats prefixes rows with terminal control sequences
// and prints control-only lines between refreshes.
func TestParseStatsLine(t *testing.T) {
	id, u, ok := parseStatsLine([]byte("\x1b[J\x1b[H{\"ID\":\"abc\",\"CPUPerc\":\"12.50%\",\"MemUsage\":\"64MiB / 256MiB\"}\x1b[K"))
	if !ok || id != "abc" || u.CPUPercent != 12.5 || u.MemoryBytes != 64<<20 || u.MemoryLimit != 256<<20 {
		t.Errorf("row = %q %+v %v", id, u, ok)
	}

	for _, line := range []string{"\x1b[H\x1b[K", "", "{not json", `{"CPUPerc":"1%"}`} {
		if _, _, ok := parseStatsLine([]byte(line)); ok {
			t.Errorf("%q parsed as a row", line)
		}
	}
}

// A row over the read limit ends the stream with an error, and the docker
// process (still writing) is killed, so the sampler can retry instead of
// waiting forever.
func TestStatsStreamOversizedRow(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")

	script := "#!/bin/sh\nhead -c " + strconv.Itoa(statsRowMax+10) + " /dev/zero | tr '\\0' x\necho\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()

	var got error
	for _, err := range (&Compose{Bin: bin}).StatsStream(ctx) {
		got = err
	}

	if got == nil || !strings.Contains(got.Error(), "read") {
		t.Errorf("err = %v, want a read error", got)
	}

	if ctx.Err() != nil || time.Since(start) > 10*time.Second {
		t.Errorf("the stream waited %v for a process it should have killed", time.Since(start))
	}
}
