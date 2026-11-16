package compose

import (
	"strings"
	"testing"

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
