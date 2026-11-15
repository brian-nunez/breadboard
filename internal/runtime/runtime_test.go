package runtime

import (
	"context"
	"errors"
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
