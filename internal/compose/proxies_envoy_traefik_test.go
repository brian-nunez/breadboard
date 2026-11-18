package compose

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// proxyLabOf is proxyLab with the proxy's type swapped.
func proxyLabOf(typ string) *spec.Spec {
	s := proxyLab()
	s.Services[1].Type = typ
	return s
}

func TestEnvoyProxyAllModesGolden(t *testing.T) {
	b := renderWith(t, proxyLabOf("envoy"), secrets.Inline, nil)
	checkCompose(t, b)
	golden(t, "envoy-proxy", b)
}

func TestTraefikAllModesGolden(t *testing.T) {
	b := renderWith(t, proxyLabOf("traefik"), secrets.Inline, nil)
	checkCompose(t, b)
	golden(t, "traefik", b)
}

// Traefik's file replaces the dynamic (routing) configuration only; the
// static configuration and the certificate store stay generated.
func TestTraefikUserFileReplacesTheRoutes(t *testing.T) {
	s := proxyLabOf("traefik")
	s.Services[1].File = "edge/dynamic.yml"
	b := renderWith(t, s, secrets.Inline, map[string]string{"edge/dynamic.yml": "# mine\n"})
	for path, mine := range map[string]bool{"config/edge/dynamic.yml": true, "config/edge/traefik.yml": false, "config/edge/tls.yml": false} {
		f, ok := b.File(path)
		if !ok || (string(f.Data) == "# mine\n") != mine {
			t.Errorf("%s: present %v, replaced %v, want replaced %v", path, ok, !mine, mine)
		}
	}
	f, _ := b.File(ComposeFile)
	for _, mount := range []string{
		"./config/edge/dynamic.yml:/etc/bb/traefik/dynamic.yml:ro",
		"./config/edge/tls.yml:/etc/bb/traefik/tls.yml:ro",
		"./config/edge/traefik.yml:/etc/bb/config/traefik.yml:ro",
	} {
		if !strings.Contains(string(f.Data), mount) {
			t.Errorf("compose lacks mount %s", mount)
		}
	}
	if strings.Contains(string(f.Data), "docker.sock") {
		t.Error("Traefik mounts the Docker socket")
	}
}

// traefikStatic is a static_file adding Prometheus metrics on the loopback
// API entry point and tracing, and replacing the log level.
const traefikStatic = `# Metrics and tracing for edge.
metrics:
  prometheus:
    # The loopback API entry point: read it with docker exec.
    entryPoint: traefik
    addRoutersLabels: true
tracing:
  serviceName: edge
  sampleRate: 0.5
log:
  level: DEBUG
`

// A static_file is merged into Traefik's generated traefik.yml: new
// sections are added after Breadboard's, log replaces Breadboard's, and
// every other file stays as generated.
func TestTraefikStaticFileGolden(t *testing.T) {
	s := proxyLabOf("traefik")
	s.Services[1].StaticFile = "edge/static.yml"
	b := renderWith(t, s, secrets.Inline, map[string]string{"edge/static.yml": traefikStatic})
	checkCompose(t, b)
	golden(t, "traefik-static", b)

	gen := renderWith(t, proxyLabOf("traefik"), secrets.Inline, nil)
	for _, name := range []string{"dynamic.yml", "tls.yml"} {
		mine, _ := b.File("config/edge/" + name)
		want, _ := gen.File("config/edge/" + name)
		if !bytes.Equal(mine.Data, want.Data) {
			t.Errorf("%s changed with a static_file", name)
		}
	}
	f, _ := b.File("config/edge/traefik.yml")
	var doc map[string]any
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatalf("merged traefik.yml is not YAML: %v", err)
	}
	if lvl := doc["log"].(map[string]any)["level"]; lvl != "DEBUG" {
		t.Errorf("log.level = %v, want DEBUG", lvl)
	}
	for _, key := range []string{"entryPoints", "providers", "api", "ping", "global", "accessLog", "metrics", "tracing"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("merged traefik.yml lacks %s", key)
		}
	}

	if hash(t, b, "edge") == hash(t, gen, "edge") {
		t.Error("the static_file does not change the config hash")
	}
	edited := renderWith(t, s, secrets.Inline, map[string]string{"edge/static.yml": strings.Replace(traefikStatic, "0.5", "1.0", 1)})
	if hash(t, edited, "edge") == hash(t, b, "edge") {
		t.Error("an edit to the static_file does not change the config hash")
	}
}

func TestTraefikStaticFileErrors(t *testing.T) {
	s := proxyLabOf("traefik")
	s.Services[1].StaticFile = "edge/static.yml"
	m, err := model.Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	render := func(content string) error {
		_, err := renderModelErr(m, secrets.Inline, func(p string) ([]byte, error) {
			if p != "edge/static.yml" {
				t.Fatalf("read %s", p)
			}
			return []byte(content), nil
		})
		return err
	}
	for _, tc := range []struct{ content, want string }{
		{"entryPoints:\n  web:\n    address: :80\n", "line 1: entryPoints is Breadboard's and cannot be set here: Breadboard's listeners"},
		{"metrics: {}\nproviders:\n  docker: {}\n", "line 2: providers is Breadboard's"},
		{"api:\n  insecure: false\n", "api is Breadboard's"},
		{"ping: {}\n", "ping is Breadboard's"},
		{"global:\n  sendAnonymousUsage: true\n", "global is Breadboard's"},
		{"EntryPoints: {}\n", "EntryPoints is Breadboard's"},
		{"tracing: {}\nTracing: {}\n", "Tracing repeats tracing"},
		{"x: &x\n  entryPoints: {}\n<<: *x\n", "merge key (<<)"},
		{"!!binary ZW50cnlQb2ludHM=: {}\n", "line 1: top-level keys must be plain section names"},
		{"!!binary cHJvdmlkZXJz: {}\n", "line 1: top-level keys must be plain section names"},
		{"!!binary YXBp: {}\n", "line 1: top-level keys must be plain section names"},
		{"!!binary cGluZw==: {}\n", "line 1: top-level keys must be plain section names"},
		{"!!binary Z2xvYmFs: {}\n", "line 1: top-level keys must be plain section names"},
		{"!!str entryPoints: {}\n", "entryPoints is Breadboard's"},
		{"&k metrics: {}\n", "without a non-string tag, anchor"},
		{"a: &a [x, x, x, x, x, x, x, x, x, x]\nb: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a, *a]\nc: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b, *b]\nd: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c, *c]\ne: &e [*d, *d, *d, *d, *d, *d, *d, *d, *d, *d]\nf: [*e, *e, *e, *e, *e, *e, *e, *e, *e, *e]\n", "expand to more than"},
		{"", "is empty"},
		{"- metrics\n", "must be a YAML mapping"},
		{"metrics: {}\n---\ntracing: {}\n", "one YAML document"},
		{"metrics: [\n", "is not valid YAML"},
	} {
		err := render(tc.content)
		if err == nil || !strings.Contains(err.Error(), "edge: static_file edge/static.yml: ") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %v, want one containing %q", tc.content, err, tc.want)
		}
	}
	_, err = renderModelErr(m, secrets.Inline, func(string) ([]byte, error) { return nil, errors.New("open edge/static.yml: no such file") })
	if err == nil || !strings.Contains(err.Error(), "edge: static_file: open edge/static.yml") {
		t.Errorf("missing file: %v", err)
	}
}
