package core

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
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
	// onUp runs at the start of each Up; a non-nil error fails that Up.
	onUp       func() error
	calls      []string
	projects   []runtime.Project
	containers map[string][]runtime.Container // by experiment
	// healthOf overrides the health of a service after Up.
	healthOf map[string]string
	stateOf  map[string]string
	subnets  []runtime.Network
	owners   []string
	logs     []runtime.LogLine
	logArgs  []string
	// filesSeen captures the compose files present when Up ran.
	filesSeen map[string][]byte
	// removedOwned records the experiment ids passed to RemoveOwned.
	removedOwned []string
	// restartOnPoll makes a service crash and restart (restart count up, new
	// start time, still running and healthy) on that Containers call.
	restartOnPoll map[string]int
	polls         int
	// execOut is what Exec prints, by service; execs records each call.
	execOut map[string]string
	execs   []string
	// imageUser is the user each service's image starts as (default root).
	imageUser map[string]runtime.ImageUser
	// published is what PublishedPorts reports.
	published []runtime.PublishedPort
	// stream feeds StatsStream: each received batch is yielded, a
	// received error ends that stream; statsCalls counts Stats calls and
	// streams counts StatsStream calls.
	stream     chan []runtime.Sample
	streamErr  chan error
	statsCalls int
	streams    int
	// streamExit delays a stream's return after ctx ends (the time docker
	// takes to die); streamsEnded counts streams that returned.
	streamExit   time.Duration
	streamsEnded int
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
	if f.onUp != nil {
		if err := f.onUp(); err != nil {
			return err
		}
	}
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

func (f *fakeRuntime) RemoveOwned(ctx context.Context, experimentID string, volumes bool) error {
	if volumes {
		f.record("remove-owned-volumes")
	} else {
		f.record("remove-owned")
	}
	f.removedOwned = append(f.removedOwned, experimentID)
	return nil
}

func (f *fakeRuntime) Exec(ctx context.Context, experiment, service string, cmd []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(cmd, " ")
	f.execs = append(f.execs, service+": "+line)
	// "service|part" matches commands containing part; "service" matches any.
	for k, out := range f.execOut {
		if svc, part, ok := strings.Cut(k, "|"); ok && svc == service && strings.Contains(line, part) {
			return []byte(out), nil
		}
	}
	if out, ok := f.execOut[service]; ok {
		return []byte(out), nil
	}
	return nil, fmt.Errorf("fake: no exec output for %s", service)
}

func (f *fakeRuntime) Build(ctx context.Context, p runtime.Project, services []string) error {
	f.record("build " + strings.Join(services, ","))
	return nil
}

func (f *fakeRuntime) ImageUser(ctx context.Context, p runtime.Project, service string) (runtime.ImageUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.imageUser[service]
	if u.User == "unresolvable" {
		return u, runtime.ErrUserUnresolved
	}
	return u, nil
}

func (f *fakeRuntime) Containers(ctx context.Context, experiment string) ([]runtime.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	list := f.containers[experiment]
	for i := range list {
		if n, ok := f.restartOnPoll[list[i].Service]; ok && f.polls == n {
			list[i].RestartCount++
			list[i].StartedAt = list[i].StartedAt.Add(time.Second)
			list[i].ExitCode = 1
		}
	}
	return list, nil
}

func (f *fakeRuntime) Stats(ctx context.Context, ids []string) (map[string]runtime.Usage, error) {
	f.mu.Lock()
	f.statsCalls++
	f.mu.Unlock()
	out := map[string]runtime.Usage{}
	for _, id := range ids {
		out[id] = runtime.Usage{CPUPercent: 1.5, MemoryBytes: 64 << 20}
	}
	return out, nil
}

func (f *fakeRuntime) StatsStream(ctx context.Context) iter.Seq2[runtime.Sample, error] {
	f.mu.Lock()
	f.streams++
	f.mu.Unlock()
	return func(yield func(runtime.Sample, error) bool) {
		defer func() {
			f.mu.Lock()
			f.streamsEnded++
			f.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				time.Sleep(f.streamExit)
				return
			case err := <-f.streamErr:
				yield(runtime.Sample{}, err)
				return
			case batch := <-f.stream:
				for _, s := range batch {
					if !yield(s, nil) {
						return
					}
				}
			}
		}
	}
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

func (f *fakeRuntime) PublishedPorts(ctx context.Context) ([]runtime.PublishedPort, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published, nil
}

// harness is an engine on a fake runtime with a private BB_HOME and project.
type harness struct {
	t   *testing.T
	eng *Engine
	rt  *fakeRuntime
	dir string
	now time.Time
	// bound are the 127.0.0.1 ports that do not bind (another program
	// holds them); no test opens a real socket.
	bound map[int]bool
}

// TestMain clears BB_DOMAIN once, so harnesses see the default domain
// without each test setting the environment, which parallel tests cannot.
func TestMain(m *testing.M) {
	os.Unsetenv("BB_DOMAIN")
	os.Exit(m.Run())
}

// parallelTests records the tests a harness already marked parallel: a test
// may build more than one harness.
var parallelTests sync.Map

// newHarness returns a harness and runs the test in parallel with the
// other harness tests: each has its own fake runtime, BB_HOME and project.
// A test that sets the environment uses newEnvHarness instead.
func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, done := parallelTests.LoadOrStore(t, true); !done {
		t.Parallel()
	}
	return newEnvHarness(t)
}

// newEnvHarness is newHarness for a test that calls t.Setenv: it runs
// alone, before the parallel tests start.
func newEnvHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, rt: newFake(), dir: filepath.Join(t.TempDir(), "lab"), now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), bound: map[int]bool{}}
	eng, err := New(Options{
		Home:          filepath.Join(t.TempDir(), "home"),
		Runtime:       h.rt,
		Now:           func() time.Time { return h.now },
		Version:       "v0.0.0-test",
		SystemRoots:   func() []byte { return []byte("-----BEGIN CERTIFICATE-----\nROOTS\n-----END CERTIFICATE-----\n") },
		HealthTimeout: time.Second,
		PollInterval:  time.Millisecond,
		StartupSettle: 20 * time.Millisecond,
		PortFree:      func(port int) bool { return !h.bound[port] },
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
