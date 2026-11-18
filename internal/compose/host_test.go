package compose

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

// hostAppLab is an app with replicas (1 for one instance) serving TLS on
// 8443, reachable from the host, and an app without host.
func hostAppLab(replicas int) *spec.Spec {
	return &spec.Spec{Version: 1, Name: "lab",
		Apps: []spec.App{
			{Name: "api", Path: "./api", Replicas: replicas, Host: &spec.Host{Port: 8443}},
			{Name: "worker", Path: "./worker"},
		},
	}
}

// hostProxyLab is an HAProxy proxy in front of an app with 2 replicas;
// only the proxy has host.
func hostProxyLab() *spec.Spec {
	return &spec.Spec{Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "edge", Type: "haproxy", Host: &spec.Host{}, Upstreams: []spec.Upstream{{Service: "web", Port: 8443}}}},
		Apps:     []spec.App{{Name: "web", Path: "./web", Replicas: 2}},
	}
}

func TestHostRouterAppReplicasGolden(t *testing.T) {
	b := renderWith(t, hostAppLab(3), secrets.Inline, nil)
	checkCompose(t, b)
	checkHostRouter(t, b)
	golden(t, "host-app-replicas", b)
}

func TestHostRouterAppSingleGolden(t *testing.T) {
	b := renderWith(t, hostAppLab(1), secrets.Inline, nil)
	checkCompose(t, b)
	checkHostRouter(t, b)
	golden(t, "host-app-single", b)
}

func TestHostRouterProxyGolden(t *testing.T) {
	b := renderWith(t, hostProxyLab(), secrets.Inline, nil)
	checkCompose(t, b)
	checkHostRouter(t, b)
	golden(t, "host-haproxy", b)
}

type hostDoc struct {
	Services map[string]struct {
		Image     string                    `yaml:"image"`
		Ports     []string                  `yaml:"ports"`
		Volumes   []string                  `yaml:"volumes"`
		Networks  map[string]map[string]any `yaml:"networks"`
		DependsOn map[string]any            `yaml:"depends_on"`
		Labels    map[string]string         `yaml:"labels"`
		MemLimit  string                    `yaml:"mem_limit"`
	} `yaml:"services"`
}

// checkHostRouter checks what D-186 to D-190 promise of bb-host: the
// catalog's Traefik pin, infra kind, the default network only, no
// depends_on, 8443 published on 127.0.0.1 only, and the host certificate
// mounted in it alone. No other service publishes a port or sees the host
// certificate.
func checkHostRouter(t *testing.T, b *Bundle) {
	t.Helper()
	f, _ := b.File(ComposeFile)
	var doc hostDoc
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}
	r, ok := doc.Services[spec.HostRouterName]
	if !ok {
		t.Fatal("no bb-host service")
	}
	traefik, _ := catalog.Lookup("traefik")
	if r.Image != traefik.Image || r.Labels[LabelKind] != "infra" || r.MemLimit != "128m" {
		t.Errorf("bb-host image %s, kind %s, memory %s", r.Image, r.Labels[LabelKind], r.MemLimit)
	}
	if !slices.Equal(r.Ports, []string{"127.0.0.1:44312:8443"}) {
		t.Errorf("bb-host ports = %v", r.Ports)
	}
	if len(r.Networks) != 1 || r.Networks[DefaultNetwork] == nil || len(r.DependsOn) != 0 {
		t.Errorf("bb-host networks %v, depends_on %v", r.Networks, r.DependsOn)
	}
	for _, m := range []string{"./tls/bb-host/host.crt:/etc/bb/host/tls.crt:ro", "./tls/bb-host/host.key:/etc/bb/host/tls.key:ro", "./ca.pem:/etc/bb/ca.pem:ro"} {
		if !slices.Contains(r.Volumes, m) {
			t.Errorf("bb-host lacks mount %s", m)
		}
	}
	if slices.ContainsFunc(r.Volumes, func(v string) bool { return strings.Contains(v, "docker.sock") }) {
		t.Error("bb-host mounts the Docker socket")
	}
	for name, svc := range doc.Services {
		if name == spec.HostRouterName {
			continue
		}
		if len(svc.Ports) > 0 {
			t.Errorf("%s publishes %v", name, svc.Ports)
		}
		if slices.ContainsFunc(svc.Volumes, func(v string) bool { return strings.Contains(v, "host.") }) {
			t.Errorf("%s mounts the host certificate: %v", name, svc.Volumes)
		}
	}
	for _, p := range []string{HostCertPath, HostKeyPath} {
		if _, ok := b.File(p); !ok {
			t.Errorf("bundle lacks %s", p)
		}
	}
}

// The host certificate is part of the router's config hash: a reissue
// recreates the router and nothing else.
func TestHostCertChangesOnlyTheRoutersHash(t *testing.T) {
	m, err := model.Resolve(hostAppLab(3), "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	render := func(hostCert string) map[string]string {
		t.Helper()
		b, err := renderHost(m, hostCert)
		if err != nil {
			t.Fatal(err)
		}
		f, _ := b.File(ComposeFile)
		var doc hostDoc
		if err := yaml.Unmarshal(f.Data, &doc); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for name, svc := range doc.Services {
			out[name] = svc.Labels[LabelConfigHash]
		}
		return out
	}
	a, b := render("ONE"), render("TWO")
	for name := range a {
		if changed := a[name] != b[name]; changed != (name == spec.HostRouterName) {
			t.Errorf("%s: config hash changed %v", name, changed)
		}
	}
}

// renderHost renders the model with a given host certificate.
func renderHost(m *model.Model, hostCert string) (*Bundle, error) {
	certs := map[string]Cert{}
	for _, svc := range m.Services {
		certs[svc.Name] = Cert{Cert: []byte("CERT"), Key: []byte("KEY")}
	}
	return Render(Input{
		Model:    m,
		Secrets:  secrets.NewMaterializer(secrets.Inline, &state.Secrets{}),
		CA:       []byte("CA"),
		Certs:    certs,
		AppDir:   func(app string) string { return "./" + app },
		HostCert: &Cert{Cert: []byte(hostCert), Key: []byte("KEY")},
		HostPort: 44312,
	})
}

func TestHostRouterNeedsCertAndPort(t *testing.T) {
	m, err := model.Resolve(hostAppLab(1), "bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	certs := map[string]Cert{}
	for _, svc := range m.Services {
		certs[svc.Name] = Cert{Cert: []byte("CERT"), Key: []byte("KEY")}
	}
	_, err = Render(Input{Model: m, Secrets: secrets.NewMaterializer(secrets.Inline, &state.Secrets{}), Certs: certs, AppDir: func(string) string { return "." }})
	if err == nil || !strings.Contains(err.Error(), "host certificate") {
		t.Errorf("err = %v", err)
	}
}
