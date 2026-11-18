package core

import (
	"bytes"
	"crypto/x509"
	"errors"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

const hostSpec = `version: 1
name: lab
services:
  edge:
    type: haproxy
    host: true
    upstreams:
      - service: api
        port: 8443
apps:
  api:
    path: ./api
    replicas: 3
    host:
      port: 8443
  worker:
    path: ./worker
`

// setupHost is a lab with an app with 3 replicas and an HAProxy proxy,
// both with host.
func setupHost(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.init()

	for _, app := range []string{"api", "worker"} {
		dir := filepath.Join(h.dir, app)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.20 AS dev\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	h.writeSpec(hostSpec)

	return h
}

func (h *harness) up() (*UpResult, []Event) {
	h.t.Helper()

	var events []Event

	res, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir, Events: func(ev Event) { events = append(events, ev) }})
	if err != nil {
		h.t.Fatal(err)
	}

	return res, events
}

func (h *harness) state() *state.ProjectState {
	h.t.Helper()

	ps, err := (state.Project{Dir: h.dir}).LoadState()
	if err != nil {
		h.t.Fatal(err)
	}

	return ps
}

func (h *harness) hostCert() (*x509.Certificate, bool) {
	h.t.Helper()

	certPEM, _, ok, err := (state.Project{Dir: h.dir}).ReadNamedCert("bb-host", "host")
	if err != nil {
		h.t.Fatal(err)
	}

	if !ok {
		return nil, false
	}

	cert, err := pki.ParseCert(certPEM)
	if err != nil {
		h.t.Fatal(err)
	}

	return cert, true
}

func TestHostPortStart(t *testing.T) {
	f := fnv.New32a()
	f.Write([]byte("lab"))

	if got, want := hostPortStart("lab"), 44300+int(f.Sum32()%700); got != want {
		t.Errorf("start = %d, want %d", got, want)
	}

	for _, name := range []string{"lab", "shop", "a", "a-very-long-experiment-name"} {
		if p := hostPortStart(name); p < 44300 || p > 44999 {
			t.Errorf("%s: start %d outside 44300-44999", name, p)
		}
	}

	if hostPortStart("lab") == hostPortStart("shop") {
		t.Log("lab and shop share a start port")
	}
}

// The first up takes the start port, records it, publishes it on bb-host
// alone, and reports every URL with its target, in route order.
func TestUpHostAccess(t *testing.T) {
	h := setupHost(t)
	res, events := h.up()

	start := hostPortStart("lab")
	if res.HostPort != start || h.state().HostPort != start {
		t.Fatalf("host port = %d, recorded %d, want %d", res.HostPort, h.state().HostPort, start)
	}

	url := func(label string) string { return hostURL(label+".lab.localhost", start) }

	want := []HostURL{
		{URL: url("edge"), Target: "edge"},
		{URL: url("api"), Target: "api"},
		{URL: url("api-1"), Target: "api", Instance: "api-1"},
		{URL: url("api-2"), Target: "api", Instance: "api-2"},
		{URL: url("api-3"), Target: "api", Instance: "api-3"},
	}
	if !slices.Equal(res.HostURLs, want) {
		t.Errorf("URLs =\n%v\nwant\n%v", res.HostURLs, want)
	}

	var infos []string

	for _, ev := range events {
		if ev.Kind == EventInfo && strings.HasPrefix(ev.Message, "Host access: ") {
			infos = append(infos, ev.Message)
		}
	}

	if len(infos) != len(want) || infos[0] != "Host access: "+url("edge")+" -> edge" || infos[2] != "Host access: "+url("api-1")+" -> api-1" {
		t.Errorf("events = %v", infos)
	}

	compose := string(h.rt.filesSeen["docker-compose.yml"])
	if strings.Count(compose, "ports:") != 1 || !strings.Contains(compose, "- 127.0.0.1:"+strconv.Itoa(start)+":8443") {
		t.Errorf("compose publishes:\n%s", compose)
	}

	rows := map[string]ServiceStatus{}
	for _, s := range res.Services {
		rows[s.Name] = s
	}

	if r := rows["bb-host"]; !r.HostRouter || r.Kind != "infra" || r.Type != "traefik" || len(r.HostURLs) != 0 {
		t.Errorf("bb-host row = %+v", r)
	}

	if got := rows["api-2"].HostURLs; !slices.Equal(got, []string{url("api"), url("api-2")}) {
		t.Errorf("api-2 URLs = %v", got)
	}

	if got := rows["edge"].HostURLs; !slices.Equal(got, []string{url("edge")}) {
		t.Errorf("edge URLs = %v", got)
	}

	if got := rows["worker"]; len(got.HostURLs) != 0 || got.HostRouter {
		t.Errorf("worker = %+v", got)
	}

	st, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(st.HostURLs, want) || st.HostPort != start {
		t.Errorf("status URLs = %v port %d", st.HostURLs, st.HostPort)
	}

	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "api-3"})
	if err != nil || !slices.Equal(d.HostURLs, []string{url("api"), url("api-3")}) || d.HostRouter {
		t.Errorf("api-3 details = %v %v %v", d.HostURLs, d.HostRouter, err)
	}

	d, err = h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "bb-host"})
	if err == nil && len(d.Env) != 0 {
		t.Errorf("bb-host details list env vars no app receives: %v", d.Env)
	}

	if err != nil || !d.HostRouter || d.Type != "traefik" || d.Certificate == nil || !slices.Equal(d.Certificate.DNSNames, []string{"bb-host.bb.internal"}) {
		t.Errorf("bb-host details = %+v %v", d, err)
	}
}

// The host certificate names exactly the lab's host names, is signed by the
// host CA (and not the lab CA), is recorded under bb-host/host, keeps its
// key at 0600, and is reissued only when the names change; removing every
// host target removes it and its record, and keeps the port.
func TestHostCertificateLifecycle(t *testing.T) {
	h := setupHost(t)
	h.up()

	cert, ok := h.hostCert()
	if !ok {
		t.Fatal("no host certificate")
	}

	names := []string{"api-1.lab.localhost", "api-2.lab.localhost", "api-3.lab.localhost", "api.lab.localhost", "edge.lab.localhost"}
	if !slices.Equal(cert.DNSNames, names) || len(cert.URIs) != 0 || !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("host certificate: SANs %v, URIs %v, EKU %v", cert.DNSNames, cert.URIs, cert.ExtKeyUsage)
	}

	if cert.Issuer.CommonName != "Breadboard host CA (localhost)" {
		t.Errorf("issuer = %s", cert.Issuer.CommonName)
	}

	ps := h.state()

	rec, ok := ps.Certificates["bb-host/host"]
	if !ok || rec.Issuer != "host" || rec.Serial != cert.SerialNumber.Text(16) {
		t.Errorf("record = %+v %v", rec, ok)
	}

	if lab, ok := ps.Certificates["bb-host"]; !ok || lab.Issuer != "root" {
		t.Errorf("bb-host lab leaf record = %+v %v", lab, ok)
	}
	// The key is 0600; the certificate is 0644, like every lab certificate.
	for file, mode := range map[string]os.FileMode{"host.key": 0o600, "host.crt": 0o644} {
		if st, err := os.Stat(filepath.Join(h.dir, ".bb/certs/bb-host", file)); err != nil || st.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want mode %v", file, st, err, mode)
		}
	}

	if bundle := h.read(".bb/runtime/tls/bb-host/host.crt"); !strings.Contains(bundle, "BEGIN CERTIFICATE") {
		t.Error("the runtime bundle lacks the host certificate")
	}

	// Same names: kept.
	h.up()

	if again, _ := h.hostCert(); again.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Error("the host certificate was reissued with the same names")
	}
	// Close to expiry: reissued.
	h.now = h.now.Add(70 * 24 * time.Hour)
	h.up()

	renewed, _ := h.hostCert()
	if renewed.SerialNumber.Cmp(cert.SerialNumber) == 0 {
		t.Error("the host certificate was not reissued near expiry")
	}

	// A changed name set (edge loses host): reissued for the new names.
	h.writeSpec(strings.Replace(hostSpec, "    host: true\n", "", 1))
	h.up()

	changed, _ := h.hostCert()
	if changed.SerialNumber.Cmp(renewed.SerialNumber) == 0 || slices.Contains(changed.DNSNames, "edge.lab.localhost") || len(changed.DNSNames) != 4 {
		t.Errorf("after edge lost host: SANs %v", changed.DNSNames)
	}

	// No host target left: no router, no host certificate, no record; the
	// port stays recorded.
	port := h.state().HostPort
	noHost := strings.Replace(strings.Replace(hostSpec, "    host: true\n", "", 1), "    host:\n      port: 8443\n", "", 1)
	h.writeSpec(noHost)

	res, _ := h.up()
	if _, ok := h.hostCert(); ok {
		t.Error("the host certificate survived the last host target")
	}

	ps = h.state()
	if _, ok := ps.Certificates["bb-host/host"]; ok {
		t.Error("the host certificate's record survived")
	}

	if _, ok := ps.Certificates["bb-host"]; ok {
		t.Error("the router's lab leaf record survived")
	}

	if ps.HostPort != port || res.HostPort != 0 || len(res.HostURLs) != 0 {
		t.Errorf("port recorded %d (was %d), result %d %v", ps.HostPort, port, res.HostPort, res.HostURLs)
	}

	if compose := string(h.rt.filesSeen["docker-compose.yml"]); strings.Contains(compose, "bb-host") || strings.Contains(compose, "ports:") {
		t.Errorf("compose still has the router:\n%s", compose)
	}

	if _, err := os.Stat(filepath.Join(h.dir, ".bb/certs/bb-host")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("certs/bb-host: %v", err)
	}
}

// A lab without host never creates the host CA.
func TestNoHostNoHostCA(t *testing.T) {
	h := setupHost(t)
	h.writeSpec(strings.Replace(strings.Replace(hostSpec, "    host: true\n", "", 1), "    host:\n      port: 8443\n", "", 1))
	h.up()

	if _, err := os.Stat(filepath.Join(h.eng.home.CADir(), "host.crt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("host CA: %v", err)
	}

	if h.state().HostPort != 0 {
		t.Error("a host port was recorded without a host target")
	}
}

func TestHostPortAllocation(t *testing.T) {
	start := hostPortStart("lab")
	next := func(p, n int) int { return 44300 + (p-44300+n)%700 }

	t.Run("taken ports are skipped", func(t *testing.T) {
		h := setupHost(t)
		h.bound[start] = true                                                                   // another program
		h.rt.published = []runtime.PublishedPort{{HostIP: "0.0.0.0", HostPort: next(start, 1)}} // another container

		res, events := h.up()
		if res.HostPort != next(start, 2) {
			t.Errorf("port = %d, want %d", res.HostPort, next(start, 2))
		}

		for _, ev := range events {
			if ev.Kind == EventWarning && strings.Contains(ev.Message, "host port") {
				t.Errorf("first allocation warned: %s", ev.Message)
			}
		}
	})

	t.Run("wraps at the end of the range", func(t *testing.T) {
		h := setupHost(t)
		for p := start; p <= 44999; p++ {
			h.bound[p] = true
		}

		if res, _ := h.up(); res.HostPort != 44300 && start != 44300 {
			t.Errorf("port = %d, want 44300", res.HostPort)
		}
	})

	t.Run("none free", func(t *testing.T) {
		h := setupHost(t)
		for p := 44300; p <= 44999; p++ {
			h.bound[p] = true
		}

		if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err == nil || !strings.Contains(err.Error(), "no free host port") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("kept while free or held by its own router", func(t *testing.T) {
		h := setupHost(t)
		h.up()
		id := h.state().ExperimentID
		// The router runs: the port no longer binds, but bb-host of this
		// lab publishes it.
		h.bound[start] = true

		h.rt.published = []runtime.PublishedPort{{HostIP: "127.0.0.1", HostPort: start, ExperimentID: id, Service: "bb-host", Running: true}}
		if res, events := h.up(); res.HostPort != start || hasWarning(events, "host port") {
			t.Errorf("port = %d (want %d), events %v", res.HostPort, start, events)
		}
		// Free again (the lab is down): kept.
		h.bound, h.rt.published = map[int]bool{}, nil
		if res, _ := h.up(); res.HostPort != start {
			t.Errorf("port = %d, want %d", res.HostPort, start)
		}
	})

	t.Run("own stopped router only reserves the port", func(t *testing.T) {
		h := setupHost(t)
		h.up()
		id := h.state().ExperimentID
		// The router container is stopped but keeps its binding; the
		// port still binds: kept, no warning.
		h.rt.published = []runtime.PublishedPort{{HostIP: "127.0.0.1", HostPort: start, ExperimentID: id, Service: "bb-host"}}
		if res, events := h.up(); res.HostPort != start || hasWarning(events, "host port") {
			t.Errorf("port = %d (want %d), events %v", res.HostPort, start, events)
		}
		// Another program took the port while the router was stopped:
		// moved with a warning.
		h.bound[start] = true

		res, events := h.up()
		if res.HostPort != next(start, 1) || h.state().HostPort != next(start, 1) {
			t.Errorf("port = %d, want %d", res.HostPort, next(start, 1))
		}

		msg := "host port " + strconv.Itoa(start) + " is taken; host access moved to " + strconv.Itoa(next(start, 1))
		if !hasWarning(events, msg) {
			t.Errorf("no warning %q in %v", msg, events)
		}
	})

	t.Run("taken by another lab's router or another program: moved with a warning", func(t *testing.T) {
		h := setupHost(t)
		h.up()
		h.rt.published = []runtime.PublishedPort{{HostIP: "127.0.0.1", HostPort: start, ExperimentID: "someone-else", Service: "bb-host"}}

		res, events := h.up()
		if res.HostPort != next(start, 1) || h.state().HostPort != next(start, 1) {
			t.Errorf("port = %d, want %d", res.HostPort, next(start, 1))
		}

		msg := "host port " + strconv.Itoa(start) + " is taken; host access moved to " + strconv.Itoa(next(start, 1))
		if !hasWarning(events, msg) {
			t.Errorf("no warning %q in %v", msg, events)
		}
		// Another program on the new port: moved again.
		h.bound[next(start, 1)] = true
		if res, events := h.up(); res.HostPort != next(start, 2) || !hasWarning(events, "host port "+strconv.Itoa(next(start, 1))+" is taken") {
			t.Errorf("port = %d, events %v", res.HostPort, events)
		}
	})
}

func hasWarning(events []Event, part string) bool {
	return slices.ContainsFunc(events, func(ev Event) bool { return ev.Kind == EventWarning && strings.Contains(ev.Message, part) })
}

// Export includes the router with the recorded port, or the start port
// without probing (nothing is recorded then).
func TestExportHostAccess(t *testing.T) {
	h := setupHost(t)
	h.bound[hostPortStart("lab")] = true // would be skipped by a probe

	out := filepath.Join(t.TempDir(), "export")
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out}); err != nil {
		t.Fatal(err)
	}

	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}
	if c := read("docker-compose.yml"); !strings.Contains(c, "- 127.0.0.1:"+strconv.Itoa(hostPortStart("lab"))+":8443") {
		t.Errorf("export lacks the start port:\n%s", c)
	}

	if !strings.Contains(read("tls/bb-host/host.crt"), "BEGIN CERTIFICATE") || !strings.Contains(read(".gitignore"), "tls/*/host.key") {
		t.Error("export lacks the host certificate or ignores its key")
	}

	if p := h.state().HostPort; p != 0 {
		t.Errorf("export recorded port %d", p)
	}

	if r := read("README.md"); !strings.Contains(r, "## Host access") || !strings.Contains(r, "- "+hostURL("api-2.lab.localhost", hostPortStart("lab"))+" -> api-2") {
		t.Errorf("README lacks the host URLs:\n%s", r)
	}
	// After an up moved it, export uses the recorded port.
	res, _ := h.up()
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out}); err != nil {
		t.Fatal(err)
	}

	if c := read("docker-compose.yml"); !strings.Contains(c, "- 127.0.0.1:"+strconv.Itoa(res.HostPort)+":8443") || res.HostPort == hostPortStart("lab") {
		t.Errorf("export lacks the recorded port %d", res.HostPort)
	}
}

// A lab domain under <experiment>.localhost is refused once host is used.
func TestHostDomainOverlapRefused(t *testing.T) {
	h := setupHost(t)
	if _, err := h.eng.ConfigSet(h.ctx(), ConfigSetRequest{Key: "domain", Value: "lab.localhost"}); err != nil {
		t.Fatal(err)
	}

	_, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "lab.localhost") {
		t.Errorf("err = %v", err)
	}
}

const hostTransitionSpec = `version: 1
name: lab
services:
  postgres:
    type: postgres
  edge:
    type: haproxy
    upstreams:
      - service: api
        port: 8443
apps:
  api:
    path: ./api
    replicas: 2
    uses: [postgres]
  web:
    path: ./web
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
`

// Turning host access on adds bb-host and its two certificates and
// changes nothing else: the root CA, every existing lab certificate and
// key, and every existing service's config hash stay byte-identical.
func TestHostAccessKeepsExistingCertificates(t *testing.T) {
	h := newHarness(t)
	h.init()

	for _, app := range []string{"api", "web"} {
		dir := filepath.Join(h.dir, app)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.20 AS dev\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	h.writeSpec(hostTransitionSpec)
	h.up()

	type snapshot struct {
		root   [2][]byte
		certs  map[string][]byte
		hashes map[string]string
	}

	take := func() snapshot {
		t.Helper()

		var s snapshot

		cert, key, ok, err := h.eng.home.LoadCA(caName)
		if err != nil || !ok {
			t.Fatalf("LoadCA(root) = %v %v", ok, err)
		}

		s.root = [2][]byte{cert, key}
		s.certs = map[string][]byte{}
		certs := filepath.Join(h.dir, ".bb/certs")

		err = filepath.WalkDir(certs, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}

			data, err := os.ReadFile(path)
			rel, _ := filepath.Rel(certs, path)
			s.certs[filepath.ToSlash(rel)] = data

			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		var doc struct {
			Services map[string]struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(h.read(".bb/runtime/docker-compose.yml")), &doc); err != nil {
			t.Fatal(err)
		}

		s.hashes = map[string]string{}
		for name, svc := range doc.Services {
			s.hashes[name] = svc.Labels["breadboard.config-hash"]
		}

		return s
	}

	before := take()
	for _, svc := range []string{"postgres", "edge", "api-1", "api-2", "web", "web-envoy"} {
		if before.hashes[svc] == "" {
			t.Fatalf("no config hash for %s in %v", svc, before.hashes)
		}
	}

	if len(before.certs) == 0 {
		t.Fatal("no lab certificates before host access")
	}

	h.writeSpec(strings.Replace(strings.Replace(hostTransitionSpec,
		"    type: haproxy\n", "    type: haproxy\n    host: true\n", 1),
		"    replicas: 2\n", "    replicas: 2\n    host:\n      port: 8443\n", 1))
	h.up()

	after := take()

	if !bytes.Equal(after.root[0], before.root[0]) || !bytes.Equal(after.root[1], before.root[1]) {
		t.Error("host access changed the root CA's files")
	}

	for name, data := range before.certs {
		if !bytes.Equal(after.certs[name], data) {
			t.Errorf("host access changed .bb/certs/%s", name)
		}
	}

	var added []string

	for name := range after.certs {
		if _, ok := before.certs[name]; !ok {
			added = append(added, name)
		}
	}

	slices.Sort(added)

	if want := []string{"bb-host/host.crt", "bb-host/host.key", "bb-host/tls.crt", "bb-host/tls.key"}; !slices.Equal(added, want) {
		t.Errorf("added certificate files = %v, want %v", added, want)
	}

	for name, hash := range before.hashes {
		if after.hashes[name] != hash {
			t.Errorf("host access changed %s's config hash: %s -> %s", name, hash, after.hashes[name])
		}
	}

	for name := range after.hashes {
		if _, ok := before.hashes[name]; !ok && name != "bb-host" {
			t.Errorf("host access added service %s", name)
		}
	}

	if after.hashes["bb-host"] == "" {
		t.Error("no bb-host service after host access")
	}
}
