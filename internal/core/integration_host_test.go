//go:build integration

package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// TestIntegrationHostAccess reaches a lab from the host through its host
// router (D-194): an app with 3 replicas serving TLS and an HAProxy proxy
// in front of it, both with host. A client on the host dials
// 127.0.0.1:<port> with SNI set to each name and the host CA as its only
// root.
func TestIntegrationHostAccess(t *testing.T) {
	eng, dir, name := sidecarLab(t, "host", `services:
  edge:
    type: haproxy
    host: true
    upstreams:
      - service: api
        port: 8443
apps:
  api:
    path: ./probe
    command: [probe, servetls, ":8443"]
    replicas: 3
    host:
      port: 8443
`, nil)

	st, err := eng.Status(context.Background(), StatusRequest{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	port := st.HostPort
	if port < hostPortFirst || port >= hostPortFirst+hostPortCount || len(st.HostURLs) != 5 {
		t.Fatalf("host port %d, URLs %v", port, st.HostURLs)
	}

	hostCAPEM, err := os.ReadFile(eng.home.CACertPath(hostCAName))
	if err != nil {
		t.Fatal(err)
	}

	labCAPEM, err := os.ReadFile(eng.home.CACertPath(caName))
	if err != nil {
		t.Fatal(err)
	}

	hostRoots, labRoots := x509.NewCertPool(), x509.NewCertPool()
	if !hostRoots.AppendCertsFromPEM(hostCAPEM) || !labRoots.AppendCertsFromPEM(labCAPEM) {
		t.Fatal("cannot read the CA certificates")
	}

	host := func(label string) string { return model.HostName(label, name) }

	// get sends GET path for name with SNI name, verifying against the
	// host CA alone (only the chain, for a name the certificate does not
	// carry), and returns the status, body and presented leaf.
	get := func(name, path string, chainOnly bool) (int, string, *x509.Certificate, error) {
		cfg := &tls.Config{RootCAs: hostRoots, ServerName: name, MinVersion: tls.VersionTLS12}
		if chainOnly {
			cfg.InsecureSkipVerify = true
			cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
				leaf, err := x509.ParseCertificate(raw[0])
				if err != nil {
					return err
				}

				_, err = leaf.Verify(x509.VerifyOptions{Roots: hostRoots})

				return err
			}
		}

		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   cfg,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
			},
		}}

		resp, err := client.Get(fmt.Sprintf("https://%s:%d%s", name, port, path))
		if err != nil {
			return 0, "", nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
			return 0, "", nil, errors.New("no peer certificate")
		}

		return resp.StatusCode, string(body), resp.TLS.PeerCertificates[0], err
	}

	t.Run("shared name reaches more than one replica", func(t *testing.T) {
		var seen map[string]int

		eventually(t, time.Minute, "api to answer from more than one replica", func() bool {
			seen = map[string]int{}

			for range 30 {
				code, body, _, err := get(host("api"), "/", false)
				if err != nil || code != http.StatusOK {
					t.Logf("api: %d %v %s", code, err, body)

					return false
				}

				seen[strings.Fields(body)[0]]++
			}

			return len(seen) > 1
		})
		t.Logf("distribution: %v", seen)

		for inst := range seen {
			if !strings.HasPrefix(inst, "instance=api-") {
				t.Errorf("unexpected answer %s", inst)
			}
		}
		// The backend sees the host's name and that the client spoke HTTPS.
		_, body, _, err := get(host("api"), "/", false)
		if want := fmt.Sprintf("proto=https host=%s:%d", host("api"), port); err != nil || !strings.Contains(body, want) {
			t.Errorf("api answered %q (%v), want %q", body, err, want)
		}
	})

	t.Run("instance names reach only their instance", func(t *testing.T) {
		for _, inst := range []string{"api-1", "api-2", "api-3"} {
			for range 5 {
				code, body, _, err := get(host(inst), "/", false)
				if err != nil || code != http.StatusOK || !strings.HasPrefix(body, "instance="+inst+" ") {
					t.Errorf("%s: %d %v %q", inst, code, err, body)
				}
			}
		}
	})

	t.Run("proxy name answers through HAProxy", func(t *testing.T) {
		path := "/via-edge-" + randomSuffix(t)

		code, body, _, err := get(host("edge"), path, false)
		if err != nil || code != http.StatusOK || !strings.HasPrefix(body, "instance=api-") {
			t.Fatalf("edge: %d %v %q", code, err, body)
		}
		// The router may reach HAProxy over HTTP/2, whose log line holds
		// the absolute URI: match the path alone.
		var logs string
		for deadline := time.Now().Add(30 * time.Second); !strings.Contains(logs, path); time.Sleep(time.Second) {
			if time.Now().After(deadline) {
				t.Fatalf("HAProxy did not log %s:\n%s", path, logs)
			}

			logs, _ = dockerMay("logs", containerOf(t, name, "edge"))
		}
	})

	t.Run("unknown name gets 404", func(t *testing.T) {
		code, _, _, err := get(host("nope"), "/", true)
		if err != nil || code != http.StatusNotFound {
			t.Errorf("unknown name: %d %v", code, err)
		}
	})

	t.Run("the host certificate does not verify against the lab CA", func(t *testing.T) {
		_, _, leaf, err := get(host("api"), "/", false)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := leaf.Verify(x509.VerifyOptions{Roots: labRoots, DNSName: host("api")}); err == nil {
			t.Error("the presented certificate verifies against the lab CA")
		}

		if leaf.Issuer.CommonName != "Breadboard host CA (localhost)" || len(leaf.URIs) != 0 {
			t.Errorf("leaf issuer %s, URIs %v", leaf.Issuer.CommonName, leaf.URIs)
		}
	})

	t.Run("the router loaded every route", func(t *testing.T) {
		out, err := execIn(t, name, spec.HostRouterName, `wget -qO- http://127.0.0.1:8080/api/http/routers`)
		for _, r := range []string{"target-edge", "target-api", "instance-api-1", "instance-api-2", "instance-api-3"} {
			if err != nil || !strings.Contains(out, `"name":"`+r+`@file"`) {
				t.Errorf("router %s not loaded: %v\n%s", r, err, out)
			}
		}

		if strings.Contains(out, `"status":"disabled"`) || strings.Contains(out, `"error"`) {
			t.Errorf("a router reports an error:\n%s", out)
		}
	})

	// curl verifies with OpenSSL, which enforces the host CA's name
	// constraints in its own way.
	t.Run("curl", func(t *testing.T) {
		curl, err := exec.LookPath("curl")
		if err != nil {
			t.Skip("curl is not installed")
		}

		for _, label := range []string{"api", "api-2", "edge"} {
			n := host(label)

			out, err := exec.Command(curl, "-sS", "--fail", "--max-time", "10",
				"--cacert", eng.home.CACertPath(hostCAName),
				"--resolve", fmt.Sprintf("%s:%d:127.0.0.1", n, port),
				fmt.Sprintf("https://%s:%d/", n, port)).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "instance=api-") {
				t.Errorf("curl %s: %v\n%s", n, err, out)
			}
		}
	})
}

// TestIntegrationHostRouterLoads runs the pinned Traefik image on the host
// router's generated configuration, with a real host certificate, and
// checks that every router loads without an error (D-194). The targets do
// not exist: loading the routes does not need them.
func TestIntegrationHostRouterLoads(t *testing.T) {
	parallelLab(t)

	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}

	name := "hostload-" + randomSuffix(t)

	s := &spec.Spec{
		Version: 1, Name: name,
		Services: []spec.Service{{Name: "edge", Type: "haproxy", Host: &spec.Host{}, Upstreams: []spec.Upstream{{Service: "api", Port: 8443}}}},
		Apps: []spec.App{
			{Name: "api", Path: "./api", Replicas: 3, Host: &spec.Host{Port: 8443}},
			{Name: "web", Path: "./web", Host: &spec.Host{Port: 9443}},
		},
	}
	if err := spec.Validate(s, catalog.Types); err != nil {
		t.Fatal(err)
	}

	m, err := model.Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}

	router, _ := m.HostRouter()
	c := catalog.HostRouter(catalog.Server{Name: router.Name, FQDN: router.FQDN, Domain: m.Domain, HostTargets: router.HostTargets})

	now := time.Now()

	hostCA, err := pki.NewHostCA(now)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := hostCA.IssueHost(catalog.HostNames(router.HostTargets), now)
	if err != nil {
		t.Fatal(err)
	}

	labCA, err := pki.NewCA(m.Domain, now)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	args := []string{"run", "-d", "--label", "breadboard.experiment=" + name}

	mount := func(file string, data []byte, target string) {
		path := filepath.Join(dir, file)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}

		args = append(args, "-v", path+":"+target+":ro")
	}
	for _, f := range c.Files {
		mount(f.Name, f.Data, f.MountPath())
	}

	mount("host.crt", leaf.CertPEM, catalog.HostCertFile)
	mount("host.key", leaf.KeyPEM, catalog.HostKeyFile)
	mount("ca.pem", labCA.CertPEM, catalog.CAFile)

	args = append(args, router.Image)
	args = append(args, c.Command...)
	id := strings.TrimSpace(docker(t, args...))
	t.Cleanup(func() {
		_, _ = dockerMay("rm", "--force", id)

		assertGone(t, name)
	})

	var out string

	eventually(t, time.Minute, "Traefik's API to list the routers", func() bool {
		out, err = dockerMay("exec", id, "wget", "-qO-", "http://127.0.0.1:8080/api/http/routers")

		return err == nil && strings.Contains(out, `"name"`)
	})

	want := []string{"target-edge", "target-api", "instance-api-1", "instance-api-2", "instance-api-3", "target-web"}
	for _, r := range want {
		if !strings.Contains(out, `"name":"`+r+`@file"`) {
			t.Errorf("router %s not loaded:\n%s", r, out)
		}
	}

	if strings.Contains(out, `"status":"disabled"`) || strings.Contains(out, `"error"`) {
		t.Errorf("a router reports an error:\n%s", out)
	}

	if health, err := dockerMay("exec", id, "traefik", "healthcheck", "--configFile=/etc/bb/config/traefik.yml", "--ping"); err != nil {
		t.Errorf("health check: %v\n%s", err, health)
	}
	// No error and no warning, the encoded-characters warning (D-204) and
	// the aliasHeadersStrategy one (D-179) included. Traefik's console log
	// marks levels as ERR and WRN.
	if logs, _ := dockerMay("logs", id); strings.Contains(logs, "Error while building configuration") ||
		strings.Contains(strings.ToLower(logs), "level=error") || strings.Contains(strings.ToLower(logs), "level=warn") ||
		strings.Contains(logs, "ERR") || strings.Contains(logs, "WRN") || strings.Contains(logs, "can reject some encoded characters") {
		t.Errorf("Traefik logged an error or a warning:\n%s", logs)
	}
}
