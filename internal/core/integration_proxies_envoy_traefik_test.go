//go:build integration

package core

import (
	"strings"
	"testing"
	"time"
)

// TestIntegrationEnvoyProxy runs the infra Envoy with one upstream in each
// TLS mode and checks what each mode promises (see proxyModesLab).
func TestIntegrationEnvoyProxy(t *testing.T) {
	proxyModesLab(t, "envoy", func(addr string) string {
		host, port, _ := strings.Cut(addr, ":")

		return `bash -c 'exec 3<>/dev/tcp/` + host + `/` + port + ` && echo open'`
	})
}

// TestIntegrationTraefik runs Traefik with one upstream in each TLS mode
// and checks what each mode promises (see proxyModesLab).
func TestIntegrationTraefik(t *testing.T) {
	name := proxyModesLab(t, "traefik", func(addr string) string {
		return `nc -z -w 3 ` + strings.Replace(addr, ":", " ", 1) + ` && echo open`
	})
	// The API is on loopback: it answers inside the container and lists
	// the generated routers, and nothing else reaches it.
	out, err := execIn(t, name, "edge", `wget -qO- http://127.0.0.1:8080/api/http/routers`)
	if err != nil || !strings.Contains(out, `"name":"default-web@file"`) || !strings.Contains(out, `"name":"route-api@file"`) {
		t.Errorf("Traefik's API: %v\n%s", err, out)
	}

	if out, err := execIn(t, name, "client", "probe dial edge.bb.internal:8080"); err == nil {
		t.Errorf("another container reached Traefik's API:\n%s", out)
	}
}

// TestIntegrationTraefikRouteNames runs Traefik with routes web and
// web-default, whose generated routers once collided (review M2), and a
// reason holding malformed and valid Go template text, which the file
// provider's template pass once failed on (review M1). Every router loads,
// and requests reach both prefixes and the fallback.
func TestIntegrationTraefikRouteNames(t *testing.T) {
	_, _, name := sidecarLab(t, "proxy-traefik-names", `services:
  edge:
    type: traefik
    upstreams:
      - service: web
        port: 8443
      - service: web-default
        tls:
          mode: off
          reason: "explore {{ unfinished\nand {{ \"valid\" }} and {{ .Values }}"
apps:
  web:
    path: ./probe
    command: [probe, servetls, ":8443"]
  web-default:
    path: ./probe
    command: [probe, serve]
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge]
`, nil)

	out, err := execIn(t, name, "edge", `wget -qO- http://127.0.0.1:8080/api/http/routers`)
	for _, want := range []string{"route-web@file", "route-web-default@file", "default-web@file"} {
		if err != nil || !strings.Contains(out, `"name":"`+want+`"`) {
			t.Errorf("router %s not loaded: %v\n%s", want, err, out)
		}
	}

	if strings.Contains(out, `"status":"disabled"`) || strings.Contains(out, `"error"`) {
		t.Errorf("a router reports an error:\n%s", out)
	}

	for path, want := range map[string]string{
		"/web/":         "instance=web ",
		"/web-default/": "instance=web-default ",
		"/elsewhere":    "instance=web ",
	} {
		eventually(t, time.Minute, path+" to reach "+strings.TrimSpace(want), func() bool {
			out, err := execIn(t, name, "client", `probe get "$EDGE_URL`+path+`"`)

			return err == nil && strings.Contains(out+" ", want)
		})
	}

	if logs := docker(t, "logs", containerOf(t, name, "edge")); strings.Contains(logs, "Error while building configuration") {
		t.Errorf("Traefik logged a configuration error:\n%s", logs)
	}
}

// proxyModesLab starts the proxy type typ with an upstream in each TLS
// mode and checks:
//
//   - re-encrypt: the client verifies the proxy's certificate, and
//     requests reach more than one of the target's 3 replicas over TLS;
//   - terminate: the app's plaintext port answers the proxy on the private
//     segment (reach returns a shell command run in the proxy that prints
//     "open" when HOST:PORT accepts a connection), and no other container
//     can reach it;
//   - passthrough: the client sees the backend's own certificate.
//
// It returns the lab's engine and experiment name for further checks.
func proxyModesLab(t *testing.T, typ string, reach func(addr string) string) string {
	t.Helper()
	_, _, name := sidecarLab(t, "proxy-"+typ, `services:
  edge:
    type: `+typ+`
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
apps:
  web:
    path: ./probe
    command: [probe, servetls, ":8443"]
    replicas: 3
  api:
    path: ./probe
    command: [probe, serve]
    replicas: 2
  payments:
    path: ./probe
    command: [probe, servetls, ":8443"]
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge]
`, nil)

	t.Run("re-encrypt", func(t *testing.T) {
		out, err := execIn(t, name, "client", `test "$EDGE_URL" = https://edge.bb.internal:8443 && probe peer edge.bb.internal:8443 edge.bb.internal`)
		if err != nil || !strings.Contains(out, "peer=edge.bb.internal") {
			t.Fatalf("the client did not verify the proxy's certificate: %v\n%s", err, out)
		}

		eventually(t, time.Minute, "requests to reach more than one replica", func() bool {
			out, err = execIn(t, name, "client", `probe hits "$EDGE_URL/web/" 30`)
			if err != nil {
				t.Logf("hits: %v %s", err, out)

				return false
			}

			return strings.Count(out, "instance=web-") >= 2
		})
		t.Logf("distribution:\n%s", out)
		// The first upstream is also the default route; the backend got
		// TLS from the proxy (it serves nothing else) and the forwarded
		// proto.
		out, err = execIn(t, name, "client", `probe get "$EDGE_URL/anything"`)
		if err != nil || !strings.Contains(out, "instance=web-") || !strings.Contains(out, "proto=https") {
			t.Errorf("default route: %v\n%s", err, out)
		}
	})

	t.Run("terminate", func(t *testing.T) {
		var out string

		eventually(t, time.Minute, "the terminated app to answer through the proxy", func() bool {
			var err error

			out, err = execIn(t, name, "client", `probe get "$EDGE_URL/api/"`)

			return err == nil && strings.Contains(out, "instance=api-")
		})
		addr, err := execIn(t, name, "api-1", `echo "$BB_PLAIN_LISTEN_ADDR"`)

		addr = strings.TrimSpace(addr)
		if err != nil || !strings.HasPrefix(addr, "10.213.") || !strings.HasSuffix(addr, ":8080") {
			t.Fatalf("api-1 BB_PLAIN_LISTEN_ADDR = %q (%v)", addr, err)
		}

		_, port, _ := strings.Cut(addr, ":")
		if out, err := execIn(t, name, "edge", reach(addr)); err != nil || !strings.Contains(out, "open") {
			t.Errorf("the proxy cannot reach the plaintext port %s: %v\n%s", addr, err, out)
		}

		for _, target := range []string{addr, "api-1.bb.internal:" + port, "api.plain.bb.internal:" + port} {
			if out, err := execIn(t, name, "client", "probe dial "+target); err == nil {
				t.Errorf("another container reached the plaintext port at %s:\n%s", target, out)
			}
		}
	})

	t.Run("passthrough", func(t *testing.T) {
		var out string

		eventually(t, time.Minute, "passthrough to reach the backend", func() bool {
			var err error

			out, err = execIn(t, name, "client", `test "$EDGE_PASSTHROUGH_ADDR" = edge.bb.internal:9443 && probe peer "$EDGE_PASSTHROUGH_ADDR" payments.bb.internal`)

			return err == nil
		})

		if !strings.Contains(out, "peer=payments.bb.internal") || strings.Contains(out, "edge.bb.internal") || !strings.Contains(out, "instance=payments proto=tls") {
			t.Errorf("the client did not see the backend's own certificate:\n%s", out)
		}

		if out, err := execIn(t, name, "client", "probe peer edge.bb.internal:9443 web.bb.internal"); err == nil {
			t.Errorf("an SNI no upstream claims was routed:\n%s", out)
		}
	})

	return name
}

// TestIntegrationTraefikStaticFile runs Traefik with a static_file that
// enables Prometheus metrics on the loopback traefik entry point (D-207).
// The merged traefik.yml loads with no warning, the health check passes,
// and /metrics, read inside the container, carries Traefik's metrics,
// including the request a client just made through the proxy.
func TestIntegrationTraefikStaticFile(t *testing.T) {
	_, _, name := sidecarLab(t, "proxy-traefik-static", `services:
  edge:
    type: traefik
    upstreams:
      - service: web
        port: 8443
    static_file: ./edge/static.yml
apps:
  web:
    path: ./probe
    command: [probe, servetls, ":8443"]
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge]
`, map[string]map[string]string{"edge": {"static.yml": `# Prometheus metrics on the loopback API entry point.
metrics:
  prometheus:
    entryPoint: traefik
    addEntryPointsLabels: true
    addServicesLabels: true
`}})
	eventually(t, time.Minute, "a request through the proxy", func() bool {
		out, err := execIn(t, name, "client", `probe get "$EDGE_URL/"`)

		return err == nil && strings.Contains(out+" ", "instance=web ")
	})

	out, err := execIn(t, name, "edge", `wget -qO- http://127.0.0.1:8080/metrics`)
	if err != nil || !strings.Contains(out, "traefik_") || !strings.Contains(out, `traefik_entrypoint_requests_total{code="200",entrypoint="websecure"`) {
		t.Errorf("Traefik's /metrics: %v\n%s", err, out)
	}

	if logs := docker(t, "logs", containerOf(t, name, "edge")); strings.Contains(logs, " WRN ") || strings.Contains(logs, " ERR ") || strings.Contains(logs, "Error while building configuration") {
		t.Errorf("Traefik logged a warning or error:\n%s", logs)
	}
}
