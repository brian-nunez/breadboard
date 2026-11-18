package spec

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// hostLookup marks the stand-in types that have an HTTPS listener, as the
// catalog does for proxies, consul and s3-compatible.
func hostLookup(id string) (TypeInfo, bool) {
	info, ok := proxyLookup(id)
	switch id {
	case "haproxy", "consul", "s3-compatible":
		info.HTTPSListener = true
	}

	return info, ok
}

func loadHost(t *testing.T, src string) (*Spec, error) {
	t.Helper()

	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	s, err := doc.Decode()
	if err != nil {
		return nil, err
	}

	return s, Validate(s, hostLookup)
}

func TestHostDecode(t *testing.T) {
	s, err := loadHost(t, `version: 1
name: lab
services:
  edge:
    type: haproxy
    upstreams: [api]
    host: true
  store:
    type: s3-compatible
    host: false
  consul:
    type: consul
    host: {}
apps:
  api:
    path: ./api
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
    host: true
  web:
    path: ./web
    host: {port: 9443}
  plain:
    path: ./plain
    host: null
`)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]*Host{"edge": {}, "store": nil, "consul": {}} {
		svc, _ := s.Service(name)
		if !reflect.DeepEqual(svc.Host, want) {
			t.Errorf("%s host = %+v, want %+v", name, svc.Host, want)
		}
	}

	for name, want := range map[string]*Host{"api": {}, "web": {Port: 9443}, "plain": nil} {
		app, _ := s.App(name)
		if !reflect.DeepEqual(app.Host, want) {
			t.Errorf("%s host = %+v, want %+v", name, app.Host, want)
		}
	}
}

func TestHostDecodeIsStrict(t *testing.T) {
	doc, err := Parse([]byte(`version: 1
name: lab
services:
  edge:
    type: haproxy
    host: yes-please
apps:
  api:
    path: ./api
    host: {port: 0, path: /}
  web:
    path: ./web
    host: {port: http}
  list:
    path: ./list
    host: [8443]
`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = doc.Decode()

	want := []string{
		"apps.api.host.path",
		"apps.api.host.port",
		"apps.list.host",
		"apps.web.host.port",
		"services.edge.host",
	}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("paths:\n got %v\nwant %v\n%v", got, want, err)
	}

	for _, msg := range []string{
		"services.edge.host: must be true, false, or a mapping with port",
		"apps.api.host.path: unknown key",
		"apps.api.host.port: must be between 1 and 65535",
		`apps.web.host.port: must be an integer, not "http"`,
		"apps.list.host: must be a mapping",
	} {
		if !strings.Contains(err.Error(), msg) {
			t.Errorf("error lacks %q:\n%v", msg, err)
		}
	}
}

func TestHostValidation(t *testing.T) {
	cases := []struct {
		name string
		src  string
		path string // empty: valid
		msg  string
	}{
		{
			name: "app with an envoy inbound port",
			src: `  api:
    path: ./api
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
    host: true`,
		},
		{
			name: "app with a register port",
			src: `  api:
    path: ./api
    uses: [consul]
    register: {port: 8443}
    host: true`,
		},
		{
			name: "app with an explicit port",
			src: `  api:
    path: ./api
    host: {port: 9443}`,
		},
		{
			name: "app without a TLS port",
			src: `  api:
    path: ./api
    host: true`,
			path: "apps.api.host",
			msg:  "app api serves no TLS port: set host.port to the port it serves TLS on, give it an envoy sidecar with inbound, or register with its TLS port",
		},
		{
			name: "app port out of range",
			src: `  api:
    path: ./api
    host: {port: 70000}`,
			path: "apps.api.host.port",
			msg:  "must be between 1 and 65535",
		},
		{
			name: "transparent app on envoy's inbound port",
			src: `  api:
    path: ./api
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
        transparent: true
    host: {port: 8443}`,
		},
		{
			name: "transparent app on its register port",
			src: `  api:
    path: ./api
    uses: [consul]
    register: {port: 9443}
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
        transparent: true
    host: {port: 9443}`,
		},
		{
			name: "transparent app on an undeclared port",
			src: `  api:
    path: ./api
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
        transparent: true
    host: {port: 8080}`,
			path: "apps.api.host.port",
			msg:  "app api runs in transparent mode, which admits only its declared TLS listeners; use its envoy inbound.port or register.port, or omit port",
		},
		{
			name: "infra services with an HTTPS listener only",
			src:  "",
		},
	}
	base := `version: 1
name: lab
services:
  consul:
    type: consul
    host: true
  store:
    type: s3-compatible
    host: true
  edge:
    type: haproxy
    host: true
apps:
`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := base + tc.src + "\n"
			if tc.src == "" {
				src = strings.TrimSuffix(base, "apps:\n")
			}

			_, err := loadHost(t, src)
			if tc.path == "" {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if got := paths(t, err); !slices.Equal(got, []string{tc.path}) {
				t.Fatalf("paths = %v, want [%s]\n%v", got, tc.path, err)
			}

			if want := tc.path + ": " + tc.msg; !strings.Contains(err.Error(), want) {
				t.Errorf("error lacks %q:\n%v", want, err)
			}
		})
	}
}

func TestHostOnInfraServices(t *testing.T) {
	_, err := loadHost(t, `version: 1
name: lab
services:
  postgres:
    type: postgres
    host: true
  pool:
    type: pooler
    upstreams: [postgres]
    host: true
  edge:
    type: haproxy
    host: {port: 8443}
`)
	want := map[string]string{
		"services.postgres.host":  "type postgres has no HTTPS listener; the host router routes only HTTP over TLS. Reach postgres from an app in the lab over its TCP listener, or publish an app's port with ports",
		"services.pool.host":      "type pooler has no HTTPS listener",
		"services.edge.host.port": "must be omitted: an infra service is reached on its catalog HTTPS listener",
	}
	got := paths(t, err)

	wantPaths := make([]string, 0, len(want))
	for p, msg := range want {
		wantPaths = append(wantPaths, p)
		if !strings.Contains(err.Error(), p+": "+msg) {
			t.Errorf("error lacks %q:\n%v", p+": "+msg, err)
		}
	}

	slices.Sort(wantPaths)

	if !slices.Equal(got, wantPaths) {
		t.Errorf("paths:\n got %v\nwant %v\n%v", got, wantPaths, err)
	}
}

func TestHostRouterNameIsReserved(t *testing.T) {
	_, err := loadHost(t, `version: 1
name: lab
services:
  bb-host:
    type: redis
apps:
  bb-host:
    path: ./a
  bb:
    path: ./bb
    sidecars:
      - type: envoy
        name: host
        inbound: {port: 8443, app_port: 8080}
`)

	want := []string{"apps.bb-host", "apps.bb.sidecars[0]", "services.bb-host"}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("paths:\n got %v\nwant %v\n%v", got, want, err)
	}

	for _, msg := range []string{
		"services.bb-host: name bb-host is reserved for the host router; choose another name",
		"apps.bb-host: name bb-host is reserved for the host router; choose another name",
		`apps.bb.sidecars[0]: makes a service named "bb-host", which is reserved for the host router; rename the app or sidecar`,
	} {
		if !strings.Contains(err.Error(), msg) {
			t.Errorf("error lacks %q:\n%v", msg, err)
		}
	}
}

func TestAppHostPort(t *testing.T) {
	envoy := Sidecar{Type: SidecarEnvoy, Envoy: &EnvoySidecar{Inbound: &EnvoyInbound{Port: 8443, AppPort: 8080}}}

	cases := []struct {
		name string
		app  App
		port int
		ok   bool
	}{
		{"no host", App{Register: &Register{Port: 9443}}, 0, false},
		{"explicit port wins", App{Host: &Host{Port: 7443}, Sidecars: []Sidecar{envoy}, Register: &Register{Port: 9443}}, 7443, true},
		{"envoy inbound before register", App{Host: &Host{}, Sidecars: []Sidecar{envoy}, Register: &Register{Port: 9443}}, 8443, true},
		{"register port", App{Host: &Host{}, Register: &Register{Port: 9443}}, 9443, true},
		{"no TLS port", App{Host: &Host{}}, 0, false},
	}
	for _, tc := range cases {
		port, ok := tc.app.HostPort()
		if port != tc.port || ok != tc.ok {
			t.Errorf("%s: HostPort() = %d, %v; want %d, %v", tc.name, port, ok, tc.port, tc.ok)
		}
	}
}

func TestAddHostRoundTrip(t *testing.T) {
	d := New("lab")

	apps := []App{
		{Name: "api", Path: "./api", Register: &Register{Port: 8443}, Host: &Host{}},
		{Name: "web", Path: "./web", Host: &Host{Port: 9443}},
		{Name: "plain", Path: "./plain"},
	}
	for _, app := range apps {
		if err := d.AddApp(app); err != nil {
			t.Fatal(err)
		}
	}

	edge := Service{Name: "edge", Type: "haproxy", Upstreams: []Upstream{{Service: "api"}}, Host: &Host{}}
	if err := d.AddProxy(edge); err != nil {
		t.Fatal(err)
	}

	out, _ := d.Bytes()
	for _, line := range []string{"    host: true\n", "    host: {port: 9443}\n"} {
		if !strings.Contains(string(out), line) {
			t.Errorf("experiment.yaml lacks %q:\n%s", line, out)
		}
	}

	doc, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}

	s, err := doc.Decode()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	for _, want := range apps {
		got, _ := s.App(want.Name)
		if !reflect.DeepEqual(got.Host, want.Host) {
			t.Errorf("%s host = %+v, want %+v\n%s", want.Name, got.Host, want.Host, out)
		}
	}

	if got, _ := s.Service("edge"); !reflect.DeepEqual(got.Host, edge.Host) {
		t.Errorf("edge host = %+v, want %+v\n%s", got.Host, edge.Host, out)
	}
}
