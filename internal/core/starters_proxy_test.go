package core

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/starters"
)

// proxyFixtureNginx is the fixture's native NGINX config: a status server
// on loopback for the health check, a route that proves this file is the
// one running, and every other request re-encrypted to the replicas of
// fixture-web by their name on the lab domain.
var proxyFixtureNginx = fmt.Sprintf(`# nginx.conf shipped by the fixture starter for {{.Experiment}}.
daemon off;
user nginx;
worker_processes 1;
pid /run/nginx.pid;
error_log /dev/stderr notice;

events {
    worker_connections 1024;
}

http {
    access_log /dev/stdout;
    resolver 127.0.0.11 valid=5s ipv6=off;

    server {
        listen 127.0.0.1:%[1]d;
        location = %[2]s {
            return 200 "ok\n";
        }
    }

    upstream fixture_web {
        zone fixture_web 64k;
        server fixture-web.{{.Domain}}:8443 resolve;
    }

    server {
        listen %[3]d ssl;
        server_name fixture-edge.{{.Domain}};
        ssl_certificate %[4]s;
        ssl_certificate_key %[5]s;

        location = /fixture {
            return 200 "shipped nginx.conf\n";
        }

        location / {
            proxy_pass https://fixture_web;
            proxy_ssl_verify on;
            proxy_ssl_trusted_certificate %[6]s;
            proxy_ssl_server_name on;
            proxy_ssl_name fixture-web.{{.Domain}};
        }
    }
}
`, catalog.NginxStatusPort, catalog.NginxHealthURI, catalog.ProxyHTTPSPort, catalog.CertFile, catalog.KeyFile, catalog.CAFile)

// proxyFixture is a starter that does not ship: two replicas of the probe
// app behind an nginx service with its own config, and a client that uses
// the proxy. Its templates are the probe app plus nginx.conf.
func proxyFixture(t *testing.T) (starters.Starter, renderFunc) {
	t.Helper()
	st := starters.Starter{
		ID:       "test/proxy",
		Template: "test/proxy",
		Dir:      "proxy-fixture",
		Apps: []starters.App{
			{Name: "fixture-web", Command: []string{"probe", "servetls", ":8443"}, Replicas: 2},
			{Name: "fixture-client", Command: []string{"probe", "serve"}, Uses: []string{"fixture-edge"}},
		},
		Services: []starters.Service{{
			Name:      "fixture-edge",
			Type:      "nginx",
			Upstreams: []spec.Upstream{{Service: "fixture-web", Port: 8443}},
			File:      "nginx.conf",
		}},
	}
	fsys := fstest.MapFS{st.Template + "/nginx.conf.tmpl": {Data: []byte(proxyFixtureNginx)}}
	src := filepath.Join("testdata", "sidecars", "probe")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fsys[path.Join(st.Template, e.Name()+".tmpl")] = &fstest.MapFile{Data: data}
	}
	return st, func(s starters.Starter, lab starters.Lab) ([]starters.File, error) {
		return starters.RenderFS(fsys, s, lab)
	}
}

// A starter's proxy service is written with its upstreams and file, the
// file is rendered with the lab domain, the result decodes and validates,
// and the export runs the shipped config. Re-applying changes nothing.
func TestApplyStarterWithProxy(t *testing.T) {
	h := newHarness(t)
	t.Setenv("BB_DOMAIN", "practice.internal")
	h.init()
	st, render := proxyFixture(t)
	res, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.AddedServices, ",") != "fixture-edge" || strings.Join(res.AddedApps, ",") != "fixture-web,fixture-client" {
		t.Fatalf("result = %+v", res)
	}
	yaml := h.read("experiment.yaml")
	if !strings.Contains(yaml, "  fixture-edge:\n    type: nginx\n    upstreams:\n      - service: fixture-web\n        port: 8443\n    file: ./proxy-fixture/nginx.conf\n") {
		t.Errorf("experiment.yaml:\n%s", yaml)
	}
	if !strings.Contains(yaml, "uses: [fixture-edge]") {
		t.Errorf("the client does not use the proxy by its name:\n%s", yaml)
	}
	doc, err := spec.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	s, err := decodeAndValidate(doc)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := serviceNamed(s, "fixture-edge")
	if svc.File != "./proxy-fixture/nginx.conf" || len(svc.Upstreams) != 1 || svc.Upstreams[0] != (spec.Upstream{Service: "fixture-web", Port: 8443}) {
		t.Errorf("decoded service = %+v", svc)
	}
	conf := h.read("proxy-fixture/nginx.conf")
	if !strings.Contains(conf, "server fixture-web.practice.internal:8443 resolve;") || !strings.Contains(conf, "for lab.") {
		t.Errorf("nginx.conf was not rendered with the domain and experiment:\n%s", conf)
	}

	out := filepath.Join(t.TempDir(), "export")
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out}); err != nil {
		t.Fatal(err)
	}
	var found bool
	_ = filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), "shipped nginx.conf") {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Error("the export does not carry the shipped nginx.conf")
	}

	again, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.AddedServices) != 0 || strings.Join(again.ExistingServices, ",") != "fixture-edge" || len(again.Written) != 0 {
		t.Errorf("re-apply = %+v", again)
	}
	if h.read("experiment.yaml") != yaml {
		t.Error("re-applying changed experiment.yaml")
	}
}

// Upstreams to a required type resolve to the experiment's service of that
// type; modes, reasons and route names are written as given.
func TestApplyStarterProxyResolvesTypes(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec("version: 1\nname: lab\nservices:\n  db:\n    type: postgres\n")
	st, render := proxyFixture(t)
	st.Requires = []string{"postgres"}
	st.Services = []starters.Service{
		{Name: "fixture-pool", Type: "pgbouncer", Upstreams: []spec.Upstream{{Service: "postgres", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "watch the pool in plaintext"}}}},
		{Name: "fixture-edge", Type: "nginx", Upstreams: []spec.Upstream{
			{Service: "fixture-web", Port: 8443, Name: "v1"},
			{Service: "fixture-web", Port: 8443, Name: "v2", TLS: spec.TLS{Mode: spec.TLSPassthrough}},
		}},
	}
	if _, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir}); err == nil || !strings.Contains(err.Error(), "upstreams[1]") {
		// The same target twice is invalid: validation runs before any file.
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "proxy-fixture")); !os.IsNotExist(err) {
		t.Fatalf("files were written before validation: %v", err)
	}
	st.Services[1].Upstreams = st.Services[1].Upstreams[:1]
	if _, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	yaml := h.read("experiment.yaml")
	for _, want := range []string{
		"  fixture-pool:\n    type: pgbouncer\n    upstreams:\n      - service: db\n        tls:\n          mode: off\n          reason: watch the pool in plaintext\n",
		"  fixture-edge:\n    type: nginx\n    upstreams:\n      - service: fixture-web\n        port: 8443\n        name: v1\n",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("experiment.yaml lacks %q:\n%s", want, yaml)
		}
	}
	if strings.Contains(yaml, "  postgres:") {
		t.Errorf("a second Postgres was added:\n%s", yaml)
	}
}

// A proxy name held by anything else (another service, or this starter's
// service from another directory) fails with ErrExists before any file.
func TestApplyStarterProxyNameTaken(t *testing.T) {
	for name, existing := range map[string]string{
		"another type":      "  fixture-edge:\n    type: haproxy\n",
		"other file":        "  fixture-edge:\n    type: nginx\n    file: ./elsewhere/nginx.conf\n",
		"a plain service":   "  fixture-edge:\n    type: redis\n",
		"generated, no app": "  fixture-edge:\n    type: nginx\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.init()
			h.writeSpec("version: 1\nname: lab\nservices:\n" + existing)
			st, render := proxyFixture(t)
			_, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir})
			if !errors.Is(err, ErrExists) {
				t.Fatalf("err = %v, want ErrExists", err)
			}
			if _, err := os.Stat(filepath.Join(h.dir, "proxy-fixture")); !os.IsNotExist(err) {
				t.Fatalf("files were written: %v", err)
			}
		})
	}

	// After the user points file elsewhere, the proxy is still recognised
	// as the starter's by its upstream to the starter's app in this directory.
	h := newHarness(t)
	h.init()
	st, render := proxyFixture(t)
	if _, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	h.writeSpec(strings.Replace(h.read("experiment.yaml"), "file: ./proxy-fixture/nginx.conf", "file: ./mine.conf", 1))
	res, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir})
	if err != nil || strings.Join(res.ExistingServices, ",") != "fixture-edge" {
		t.Fatalf("re-apply after editing file: %+v, %v", res, err)
	}
}

// Only a client the starter declares counts: an app of the user's in the
// starter's directory that uses the proxy does not make it the starter's.
func TestApplyStarterProxyUnrelatedClient(t *testing.T) {
	h := newHarness(t)
	h.init()
	if err := os.MkdirAll(filepath.Join(h.dir, "proxy-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.writeSpec("version: 1\nname: lab\nservices:\n  fixture-edge:\n    type: nginx\n    file: ./elsewhere/nginx.conf\napps:\n  mine:\n    path: ./proxy-fixture\n    uses: [fixture-edge]\n")
	st, render := proxyFixture(t)
	if _, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "proxy-fixture", "nginx.conf")); !os.IsNotExist(err) {
		t.Fatalf("files were written: %v", err)
	}
}

// A service file that the template does not render is a starter bug,
// reported before anything is written.
func TestApplyStarterProxyFileMissing(t *testing.T) {
	h := newHarness(t)
	h.init()
	st, render := proxyFixture(t)
	st.Services[0].File = "haproxy.cfg"
	if _, err := h.eng.applyStarter(h.ctx(), st, render, ApplyStarterRequest{Dir: h.dir}); err == nil || !strings.Contains(err.Error(), "haproxy.cfg.tmpl") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(h.read("experiment.yaml"), "fixture-edge") {
		t.Fatal("experiment.yaml was written")
	}
}
