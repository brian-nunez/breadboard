package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
	"github.com/brian-nunez/breadboard/internal/runtime"
)

// fakeRuntime is a Docker that is available, runs nothing, records the
// log tail it was asked for, and prints exec output set per service.
type fakeRuntime struct {
	mu   sync.Mutex
	tail int
	exec map[string][]byte
}

func (*fakeRuntime) Available(context.Context) error                        { return nil }
func (*fakeRuntime) Up(context.Context, runtime.Project) error              { return nil }
func (*fakeRuntime) Down(context.Context, runtime.Project, bool) error      { return nil }
func (*fakeRuntime) RemoveOwned(context.Context, string, bool) error        { return nil }
func (*fakeRuntime) Subnets(context.Context) ([]runtime.Network, error)     { return nil, nil }
func (*fakeRuntime) Build(context.Context, runtime.Project, []string) error { return nil }
func (*fakeRuntime) ImageUser(context.Context, runtime.Project, string) (runtime.ImageUser, error) {
	return runtime.ImageUser{}, nil
}
func (*fakeRuntime) Owners(context.Context, string) ([]string, error) { return nil, nil }
func (*fakeRuntime) PublishedPorts(context.Context) ([]runtime.PublishedPort, error) {
	return nil, nil
}
func (f *fakeRuntime) Exec(_ context.Context, _, service string, _ []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if out, ok := f.exec[service]; ok {
		return out, nil
	}
	return nil, errors.New("not running")
}
func (*fakeRuntime) Containers(context.Context, string) ([]runtime.Container, error) {
	return nil, nil
}
func (*fakeRuntime) StatsStream(context.Context) iter.Seq2[runtime.Sample, error] {
	return func(func(runtime.Sample, error) bool) {}
}
func (*fakeRuntime) Stats(context.Context, []string) (map[string]runtime.Usage, error) {
	return map[string]runtime.Usage{}, nil
}
func (f *fakeRuntime) Logs(_ context.Context, _ runtime.Project, _ []string, _ bool, tail int) iter.Seq2[runtime.LogLine, error] {
	f.mu.Lock()
	f.tail = tail
	f.mu.Unlock()
	return func(yield func(runtime.LogLine, error) bool) {
		yield(runtime.LogLine{Service: "postgres", Text: "ready to accept connections"}, nil)
	}
}

type harness struct {
	root string
	rt   *fakeRuntime
	cs   *sdk.ClientSession

	mu       sync.Mutex
	progress []string
}

func newHarness(t *testing.T, readOnly bool) *harness {
	t.Helper()
	h := &harness{root: t.TempDir(), rt: &fakeRuntime{}}
	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: h.rt, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(eng, Options{Dir: h.root, ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			h.mu.Lock()
			h.progress = append(h.progress, req.Params.Message)
			h.mu.Unlock()
		},
	})
	if h.cs, err = client.Connect(ctx, ct, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.cs.Close(); ss.Wait() })
	return h
}

// call runs a tool and returns its structured result and error text.
func (h *harness) call(t *testing.T, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	params := &sdk.CallToolParams{Name: name, Arguments: args}
	params.SetProgressToken("p")
	res, err := h.cs.CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msg []string
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				msg = append(msg, tc.Text)
			}
		}
		return nil, strings.Join(msg, "\n")
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: structured content: %v", name, err)
	}
	return out, ""
}

func (h *harness) mustCall(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	out, errText := h.call(t, name, args)
	if errText != "" {
		t.Fatalf("%s: %s", name, errText)
	}
	return out
}

func (h *harness) tools(t *testing.T) map[string]*sdk.Tool {
	t.Helper()
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		m[tool.Name] = tool
	}
	return m
}

func TestCAReturnsHostCA(t *testing.T) {
	h := newHarness(t, true)
	out := h.mustCall(t, "ca", nil)
	if out["name"] != "root" || out["domain"] != "bb.internal" {
		t.Fatalf("ca root = %v %v", out["name"], out["domain"])
	}
	host, ok := out["host"].(map[string]any)
	if !ok {
		t.Fatalf("ca has no host object: %v", out)
	}
	for key, want := range map[string]any{"name": "host", "domain": "localhost", "subject": "Breadboard host CA (localhost)", "created": true} {
		if host[key] != want {
			t.Errorf("host.%s = %v, want %v", key, host[key], want)
		}
	}
	for _, key := range []string{"not_before", "not_after", "fingerprint", "cert_path", "cert_pem"} {
		if v, _ := host[key].(string); v == "" {
			t.Errorf("host.%s is empty", key)
		}
	}
	if pemText, _ := host["cert_pem"].(string); host["fingerprint"] == out["fingerprint"] || !strings.HasPrefix(pemText, "-----BEGIN CERTIFICATE-----") {
		t.Error("host CA is not a distinct certificate")
	}
	again, _ := h.mustCall(t, "ca", nil)["host"].(map[string]any)
	if again["created"] != false || again["fingerprint"] != host["fingerprint"] {
		t.Error("a second ca call replaced the host CA")
	}
}

func TestGuidesAndStarterCode(t *testing.T) {
	h := newHarness(t, true)
	if g := h.mustCall(t, "guide", nil)["guide"].(string); !strings.Contains(g, "experiment.yaml") {
		t.Errorf("guide = %q", g)
	}
	if n := h.mustCall(t, "learn", map[string]any{"type": "kafka"})["notes"].(string); n == "" {
		t.Error("learn kafka: no notes")
	}
	d := h.mustCall(t, "starter_details", map[string]any{"id": "kafka/producer-consumer"})
	var dockerfile string
	for _, f := range d["files"].([]any) {
		if f := f.(map[string]any); strings.HasSuffix(f["path"].(string), "Dockerfile") {
			dockerfile = f["content"].(string)
		}
	}
	if !strings.Contains(dockerfile, "FROM") {
		t.Errorf("starter Dockerfile = %q, want its text", dockerfile)
	}
	// Proxies the starter adds: a list (empty here), never null.
	if svcs, ok := d["services"].([]any); !ok || len(svcs) != 0 {
		t.Errorf("starter services = %v", d["services"])
	}
	for _, e := range h.mustCall(t, "catalog", nil)["entries"].([]any) {
		if e := e.(map[string]any); !strings.Contains(e["image"].(string), ":") {
			t.Errorf("%s: image %q is not pinned", e["type"], e["image"])
		}
	}
}
