package core

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/runtime"
)

// fakeRuntime records calls and simulates containers. By default every
// service becomes healthy as soon as Up runs.
type fakeRuntime struct {
	mu          sync.Mutex
	unavailable error
	upErr       error
	calls       []string
	projects    []runtime.Project
	containers  map[string][]runtime.Container // by experiment
	// healthOf overrides the health of a service after Up.
	healthOf map[string]string
	stateOf  map[string]string
	subnets  []runtime.Network
	owners   []string
	logs     []runtime.LogLine
	logArgs  []string
	// filesSeen captures the compose files present when Up ran.
	filesSeen map[string][]byte
}

func newFake() *fakeRuntime {
	return &fakeRuntime{containers: map[string][]runtime.Container{}, healthOf: map[string]string{}, stateOf: map[string]string{}}
}

func (f *fakeRuntime) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeRuntime) Available(ctx context.Context) error {
	f.record("available")
	return f.unavailable
}

func (f *fakeRuntime) Up(ctx context.Context, p runtime.Project) error {
	f.record("up")
	if f.upErr != nil {
		return f.upErr
	}
	f.projects = append(f.projects, p)
	f.filesSeen = map[string][]byte{}
	for _, file := range p.Files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		f.filesSeen[filepath.Base(file)] = data
	}
	experiment := p.Name[len("bb-"):]
	var list []runtime.Container
	for _, svc := range servicesInCompose(f.filesSeen["docker-compose.yml"]) {
		c := runtime.Container{ID: "id-" + svc.name, Service: svc.name, Kind: svc.kind, State: "running", Health: "healthy", StartedAt: time.Unix(0, 0)}
		if h, ok := f.healthOf[svc.name]; ok {
			c.Health = h
		}
		if s, ok := f.stateOf[svc.name]; ok {
			c.State = s
		}
		list = append(list, c)
	}
	f.containers[experiment] = list
	return nil
}

func (f *fakeRuntime) Down(ctx context.Context, p runtime.Project, volumes bool) error {
	if volumes {
		f.record("down-volumes")
	} else {
		f.record("down")
	}
	delete(f.containers, p.Name[len("bb-"):])
	return nil
}

func (f *fakeRuntime) Containers(ctx context.Context, experiment string) ([]runtime.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers[experiment], nil
}

func (f *fakeRuntime) Stats(ctx context.Context, ids []string) (map[string]runtime.Usage, error) {
	out := map[string]runtime.Usage{}
	for _, id := range ids {
		out[id] = runtime.Usage{CPUPercent: 1.5, MemoryBytes: 64 << 20}
	}
	return out, nil
}

func (f *fakeRuntime) Logs(ctx context.Context, p runtime.Project, services []string, follow bool, tail int) iter.Seq2[runtime.LogLine, error] {
	f.logArgs = services
	return func(yield func(runtime.LogLine, error) bool) {
		for _, l := range f.logs {
			if !yield(l, nil) {
				return
			}
		}
	}
}

func (f *fakeRuntime) Subnets(ctx context.Context) ([]runtime.Network, error) {
	return f.subnets, nil
}

func (f *fakeRuntime) Owners(ctx context.Context, experiment string) ([]string, error) {
	return f.owners, nil
}

// harness is an engine on a fake runtime with a private BB_HOME and project.
type harness struct {
	t   *testing.T
	eng *Engine
	rt  *fakeRuntime
	dir string
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("BB_DOMAIN", "")
	os.Unsetenv("BB_DOMAIN")
	h := &harness{t: t, rt: newFake(), dir: filepath.Join(t.TempDir(), "lab"), now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	eng, err := New(Options{
		Home:          filepath.Join(t.TempDir(), "home"),
		Runtime:       h.rt,
		Now:           func() time.Time { return h.now },
		Version:       "v0.0.0-test",
		SystemRoots:   func() []byte { return []byte("-----BEGIN CERTIFICATE-----\nROOTS\n-----END CERTIFICATE-----\n") },
		HealthTimeout: time.Second,
		PollInterval:  time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.eng = eng
	return h
}

func (h *harness) ctx() context.Context { return context.Background() }

func (h *harness) init() {
	h.t.Helper()
	if _, err := h.eng.Init(h.ctx(), InitRequest{Dir: h.dir, Name: "lab"}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) add(typ string) {
	h.t.Helper()
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: typ}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) writeSpec(src string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, "experiment.yaml"), []byte(src), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) read(rel string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dir, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

type composeService struct{ name, kind string }

// servicesInCompose extracts service names and kinds from a rendered file.
func servicesInCompose(data []byte) []composeService {
	var doc struct {
		Services map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	_ = yaml.Unmarshal(data, &doc)
	var out []composeService
	for name, s := range doc.Services {
		out = append(out, composeService{name: name, kind: s.Labels["breadboard.kind"]})
	}
	return out
}
