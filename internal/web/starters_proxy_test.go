package web

import (
	"bytes"
	"context"
	"html"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// A starter that brings a proxy shows it in experiment.yaml as bb starter
// writes it, draws it in front of its upstreams, and shows its native config.
func TestStarterPageShowsProxies(t *testing.T) {
	d := &core.StarterDetails{
		StarterInfo: core.StarterInfo{ID: "lb/example", Title: "Example", Requires: []string{"postgres"}},
		Dir:         "example",
		Apps: []core.StarterApp{
			{Name: "lb-web", Command: []string{"lb-web"}, Replicas: 2},
			{Name: "lb-client", Command: []string{"lb-client"}, Uses: []string{"lb-edge"}},
		},
		Services: []core.StarterService{
			{Name: "lb-edge", Type: "nginx", File: "nginx.conf", Upstreams: []spec.Upstream{
				{Service: "lb-web", Port: 8443},
				{Service: "lb-web", Name: "plain", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "watch it in plaintext"}},
			}},
			{Name: "lb-pool", Type: "pgbouncer", Upstreams: []spec.Upstream{{Service: "postgres"}}},
		},
		Files: []core.StarterFile{{Path: "nginx.conf", Data: []byte("upstream lb_web { server lb-web.bb.internal:8443 resolve; }\n")}},
	}
	yaml := specAdditions(d)
	for _, want := range []string{
		"  lb-edge:\n    type: nginx\n    upstreams:\n      - service: lb-web\n        port: 8443\n      - service: lb-web\n        name: plain\n        tls:\n          mode: off\n          reason: watch it in plaintext\n    file: ./example/nginx.conf\n",
		"  lb-pool:\n    type: pgbouncer\n    upstreams: [postgres]\n",
		"    uses: [lb-edge]\n",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("spec additions lack %q:\n%s", want, yaml)
		}
	}
	cat := map[string]core.CatalogEntry{
		"nginx":     {Type: "nginx", Title: "NGINX", Ports: []int{8443}},
		"pgbouncer": {Type: "pgbouncer", Title: "PgBouncer", Ports: []int{6432}},
		"postgres":  {Type: "postgres", Title: "PostgreSQL", Ports: []int{5432}},
	}
	rows := starterRows(d, cat)
	if !hasProxies(rows) {
		t.Fatal("no proxy row")
	}
	for _, r := range rows {
		switch r.Name {
		case "lb-edge":
			if len(r.Uses) != 2 || r.Uses[0].Port != 8443 || r.Uses[1].Port != spec.DefaultPlainPort || r.Uses[1].Mode != spec.TLSOff || !r.Uses[0].ToApp {
				t.Errorf("lb-edge edges = %+v", r.Uses)
			}
		case "lb-pool":
			if len(r.Uses) != 1 || r.Uses[0].Port != 5432 || r.Uses[0].ToApp {
				t.Errorf("lb-pool edges = %+v", r.Uses)
			}
		case "lb-client":
			if len(r.Uses) != 1 || r.Uses[0].To != "lb-edge" || r.Uses[0].Port != 8443 {
				t.Errorf("lb-client edges = %+v", r.Uses)
			}
		}
	}

	v := starterView{Details: d, Catalog: cat, Spec: yaml, Configs: starterConfigs(d)}
	if len(v.Configs) != 1 || v.Configs[0].Service != "lb-edge" {
		t.Fatalf("configs = %+v", v.Configs)
	}
	v.Graph = layout(rows, "")
	v.Graph.Static = true
	var buf bytes.Buffer
	if err := starterPage(v).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	body := html.UnescapeString(buf.String())
	for _, want := range []string{"Proxy configs", "example/nginx.conf", "server lb-web.bb.internal:8443 resolve;", "PROXIES"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}
