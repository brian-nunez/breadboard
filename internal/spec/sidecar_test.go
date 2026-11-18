package spec

import (
	"slices"
	"strings"
	"testing"
)

// sidecarLookup extends the stand-in catalog with the types sidecars refer to.
func sidecarLookup(id string) (TypeInfo, bool) {
	switch id {
	case "s3-compatible":
		return TypeInfo{HTTPS: true}, true
	case "consul":
		return TypeInfo{Registry: true, HTTPS: true, Plaintext: true}, true
	case "postgres":
		info, _ := types(id)
		info.Env = func(string, bool) []string {
			return []string{"DATABASE_URL", "PGHOST", "PGPORT", "PGUSER", "PGPASSWORD"}
		}

		return info, true
	}

	return types(id)
}

func loadWith(t *testing.T, src string) (*Spec, error) {
	t.Helper()

	doc, err := Parse([]byte(src))
	if err != nil {
		return nil, err
	}

	s, err := doc.Decode()
	if err != nil {
		return nil, err
	}

	return s, Validate(s, sidecarLookup)
}

const sidecarsExample = `version: 1
name: lab
services:
  postgres:
    type: postgres
  s3:
    type: s3-compatible
  consul:
    type: consul
apps:
  api:
    path: ./api
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  web:
    path: ./web
    uses: [postgres, s3, consul]
    register: {port: 9443}
    sidecars:
      - type: envoy
        outbound:
          - service: api
            port: 9001
            lb: least_request
            timeout: 2s
            retries: 0
            outlier: {consecutive_5xx: 5, ejection: 30s, max_ejection_percent: 50, interval: 5s}
          - service: consul
            port: 9002
        transparent: {egress: deny}
        file: ./envoy.yaml
      - type: pgbouncer
        service: postgres
        pool_mode: session
      - type: fluent-bit
        name: logs
        to: s3
        bucket: app-logs
`

func TestSidecarsDecode(t *testing.T) {
	s, err := loadWith(t, sidecarsExample)
	if err != nil {
		t.Fatal(err)
	}

	api, _ := s.App("api")
	if api.Replicas != 3 || !slices.Equal(api.Instances(), []string{"api-1", "api-2", "api-3"}) || !api.IsPod() {
		t.Errorf("api = %+v instances %v", api, api.Instances())
	}

	if p, ok := api.TLSPort(); !ok || p != 8443 {
		t.Errorf("api TLS port = %d %v", p, ok)
	}

	web, _ := s.App("web")
	if len(web.Sidecars) != 3 {
		t.Fatalf("web sidecars = %+v", web.Sidecars)
	}

	e := web.Sidecars[0].Envoy
	if e == nil || len(e.Outbound) != 2 || e.Transparent == nil || e.Transparent.Egress != EgressDeny || e.File != "./envoy.yaml" {
		t.Fatalf("envoy = %+v", e)
	}

	o := e.Outbound[0]
	if o.Service != "api" || o.Port != 9001 || o.LB != "least_request" || o.Timeout != "2s" || o.Retries == nil || *o.Retries != 0 || *o.Outlier != (Outlier{Consecutive5xx: 5, Ejection: "30s", MaxEjectionPercent: 50, Interval: "5s"}) {
		t.Errorf("outbound = %+v", o)
	}

	if pb := web.Sidecars[1].PgBouncer; pb == nil || *pb != (PgBouncerSidecar{Service: "postgres", PoolMode: "session"}) {
		t.Errorf("pgbouncer = %+v", pb)
	}

	if fb := web.Sidecars[2]; fb.ID() != "logs" || fb.FluentBit == nil || *fb.FluentBit != (FluentBitSidecar{To: "s3", Bucket: "app-logs"}) {
		t.Errorf("fluent-bit = %+v", fb)
	}

	if p, ok := web.TLSPort(); !ok || p != 9443 {
		t.Errorf("web TLS port (register) = %d %v", p, ok)
	}
}

func TestSidecarDefaults(t *testing.T) {
	sc := Sidecar{Type: SidecarPgBouncer, PgBouncer: &PgBouncerSidecar{Service: "pg"}}.WithDefaults()
	if sc.Name != "pgbouncer" || *sc.PgBouncer != (PgBouncerSidecar{Service: "pg", PoolMode: "transaction", Port: 6432, PoolSize: 10}) {
		t.Errorf("pgbouncer defaults = %+v %+v", sc, sc.PgBouncer)
	}

	in := Sidecar{Type: SidecarEnvoy, Envoy: &EnvoySidecar{Outbound: []EnvoyOutbound{{Service: "a", Port: 1, Outlier: &Outlier{Consecutive5xx: 3, Ejection: "30s"}}}, Transparent: &Transparent{}}}

	e := in.WithDefaults().Envoy
	if o := e.Outbound[0]; o.LB != "round_robin" || o.Timeout != "5s" || *o.Retries != 2 {
		t.Errorf("envoy outbound defaults = %+v", o)
	}
	// Envoy's own outlier defaults, written out so the generated config shows them.
	if l := *e.Outbound[0].Outlier; l != (Outlier{Consecutive5xx: 3, Ejection: "30s", MaxEjectionPercent: 10, Interval: "10s"}) {
		t.Errorf("outlier defaults = %+v", l)
	}

	if l := *in.Envoy.Outbound[0].Outlier; l.MaxEjectionPercent != 0 || l.Interval != "" {
		t.Error("WithDefaults changed its input outlier")
	}

	if e.Transparent.Egress != EgressAllow {
		t.Errorf("transparent egress default = %q", e.Transparent.Egress)
	}

	if in.Envoy.Outbound[0].Retries != nil || in.Envoy.Transparent.Egress != "" {
		t.Error("WithDefaults changed its input")
	}

	if (App{}).ReplicaCount() != 1 || !slices.Equal((App{Name: "a"}).Instances(), []string{"a"}) {
		t.Error("an app without replicas must run one instance under its own name")
	}
}

func TestSidecarDecodeIsStrict(t *testing.T) {
	src := `version: 1
name: lab
apps:
  a:
    path: ./a
    replicas: 0
    sidecars:
      - inbound: {port: 1}
      - type: consul-agent
      - type: nginx
      - type: pgbouncer
        transparent: true
        colour: blue
      - type: fluent-bit
      - type: envoy
        inbound: {port: 8443, extra: 1}
        outbound:
          - port: x
            outlier: {max_ejection_percent: 0, interval: 5}
        transparent: maybe
      - just-a-string
  b:
    path: ./b
    sidecars: {type: envoy}
`

	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	_, err = doc.Decode()

	want := []string{
		"apps.a.replicas",
		"apps.a.sidecars[0].type",
		"apps.a.sidecars[1].type",
		"apps.a.sidecars[2].type",
		"apps.a.sidecars[3].colour",
		"apps.a.sidecars[3].service",
		"apps.a.sidecars[3].transparent",
		"apps.a.sidecars[4].bucket",
		"apps.a.sidecars[4].to",
		"apps.a.sidecars[5].inbound.app_port",
		"apps.a.sidecars[5].inbound.extra",
		"apps.a.sidecars[5].outbound[0].outlier.consecutive_5xx",
		"apps.a.sidecars[5].outbound[0].outlier.ejection",
		"apps.a.sidecars[5].outbound[0].outlier.max_ejection_percent",
		"apps.a.sidecars[5].outbound[0].port",
		"apps.a.sidecars[5].outbound[0].service",
		"apps.a.sidecars[5].transparent",
		"apps.a.sidecars[6]",
		"apps.b.sidecars",
	}
	if got := paths(t, err); !slices.Equal(got, want) {
		t.Fatalf("violation paths:\n got %v\nwant %v", got, want)
	}

	for _, s := range []string{"use register", `unknown sidecar type "nginx"`, "only supported on envoy sidecars", "unknown key for a pgbouncer sidecar"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error lacks %q:\n%v", s, err)
		}
	}
}

func TestSidecarValidation(t *testing.T) {
	src := `version: 1
name: lab
services:
  postgres:
    type: postgres
  cache:
    type: redis
  s3:
    type: s3-compatible
  consul:
    type: consul
apps:
  plain:
    path: ./plain
  api:
    path: ./api
    replicas: 11
  many:
    path: ./many
    replicas: 2
    ports: [8080]
  web:
    path: ./web
    uses:
      - cache
      - service: postgres
        tls: {mode: off, reason: test}
      - consul
    register: {port: 8443}
    ports: [8080]
    sidecars:
      - type: envoy
        name: Bad_Name
        inbound: {port: 8443, app_port: 8443}
        outbound:
          - service: ghost
            port: 9001
          - service: web
            port: 9002
          - service: plain
            port: 9003
          - service: cache
            port: 9004
          - service: cache
            port: 9005
            lb: fastest
            timeout: soon
            retries: 11
            outlier: {consecutive_5xx: 0, ejection: "-1s", max_ejection_percent: 101, interval: soon}
          - service: consul
            port: 15000
        transparent: {egress: maybe}
        file: /etc/envoy.yaml
      - type: envoy
        inbound: {port: 9443, app_port: 8080}
      - type: pgbouncer
        service: cache
        pool_mode: lazy
        port: 70000
        pool_size: -1
      - type: pgbouncer
        name: pgbouncer
        service: postgres
        port: 9443
      - type: fluent-bit
        to: missing
        bucket: A_Bucket
      - type: fluent-bit
        name: logs
        to: postgres
        bucket: ok-bucket
  pod:
    path: ./pod
    sidecars:
      - type: fluent-bit
        to: s3
        bucket: logs
  pod-pod:
    path: ./x
  web-envoy:
    path: ./y
`
	_, err := loadWith(t, src)
	want := []string{
		"apps.api.replicas",
		"apps.many.ports",
		"apps.pod-pod", // collides with the pod holder of app pod
		"apps.pod.sidecars[0].to",
		"apps.web-envoy", // collides with web's envoy sidecar
		"apps.web.sidecars[0].file",
		"apps.web.sidecars[0].inbound.app_port",
		"apps.web.sidecars[0].name",
		"apps.web.sidecars[0].outbound[0].service",
		"apps.web.sidecars[0].outbound[1].service",
		"apps.web.sidecars[0].outbound[2].service",
		"apps.web.sidecars[0].outbound[3].service",
		"apps.web.sidecars[0].outbound[4].lb",
		"apps.web.sidecars[0].outbound[4].outlier.consecutive_5xx",
		"apps.web.sidecars[0].outbound[4].outlier.ejection",
		"apps.web.sidecars[0].outbound[4].outlier.interval",
		"apps.web.sidecars[0].outbound[4].outlier.max_ejection_percent",
		"apps.web.sidecars[0].outbound[4].retries",
		"apps.web.sidecars[0].outbound[4].service",
		"apps.web.sidecars[0].outbound[4].timeout",
		"apps.web.sidecars[0].outbound[5].port",
		"apps.web.sidecars[0].transparent.egress",
		"apps.web.sidecars[1]",         // its admin port collides with the first envoy's
		"apps.web.sidecars[1].inbound", // a second BB_LISTEN_ADDR
		"apps.web.sidecars[1].inbound.app_port",
		"apps.web.sidecars[2].pool_mode",
		"apps.web.sidecars[2].pool_size",
		"apps.web.sidecars[2].port",
		"apps.web.sidecars[2].service",
		"apps.web.sidecars[3]", // duplicate name
		"apps.web.sidecars[3].port",
		"apps.web.sidecars[3].service",
		"apps.web.sidecars[4].bucket",
		"apps.web.sidecars[4].to",
		"apps.web.sidecars[5]",
		"apps.web.sidecars[5].to",
		"apps.web.sidecars[5].to",
	}

	got := paths(t, err)
	if !slices.Equal(got, want) {
		t.Fatalf("violation paths:\n got %v\nwant %v\n%v", got, want, err)
	}

	for _, s := range []string{
		"must be between 1 and 10",
		"cannot be published by more than one replica",
		"must name a service or app",
		"must not be the app itself",
		"app plain serves no TLS port",
		"cache (redis) has no TLS HTTP listener",
		"already has an outbound listener",
		"port 15000 is already used in this pod by sidecar Bad_Name (admin)",
		"duplicate sidecar name",
		"must be a postgres service, not redis",
		"must be an s3-compatible service in this app's uses",
		"over TLS; remove tls.mode: off",
		"must be an s3-compatible service, not postgres",
		"cannot be published; publish inbound.port instead",
	} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error lacks %q:\n%v", s, err)
		}
	}
}

func TestReplicasRoundTrip(t *testing.T) {
	d := New("lab")
	if err := d.AddApp(App{Name: "api", Path: "./api", Replicas: 3}); err != nil {
		t.Fatal(err)
	}

	out, _ := d.Bytes()

	s, err := loadWith(t, string(out))
	if err != nil || s.Apps[0].Replicas != 3 {
		t.Fatalf("round trip = %+v %v\n%s", s, err, out)
	}
}

// An outbound target named database would give the app a DATABASE_URL that
// overwrites Postgres's: refused, naming both declarations, whatever the
// sidecar order and with or without PgBouncer (which rewrites only its own
// service's variables).
func TestSidecarEnvCollisions(t *testing.T) {
	const head = `version: 1
name: lab
services:
  postgres:
    type: postgres
apps:
  database:
    path: ./db
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  api:
    path: ./api
    uses: [postgres]
    sidecars:
`

	envoy := "      - type: envoy\n        outbound:\n          - service: database\n            port: 9001\n"

	pooler := "      - type: pgbouncer\n        service: postgres\n"
	for name, c := range map[string]struct{ sidecars, path string }{
		"envoy only":           {envoy, "apps.api.sidecars[0].outbound[0].service"},
		"envoy then pgbouncer": {envoy + pooler, "apps.api.sidecars[0].outbound[0].service"},
		"pgbouncer then envoy": {pooler + envoy, "apps.api.sidecars[1].outbound[0].service"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadWith(t, head+c.sidecars)
			if got := paths(t, err); !slices.Equal(got, []string{c.path}) {
				t.Fatalf("violations = %v (%v)", got, err)
			}

			if msg := err.Error(); !strings.Contains(msg, "gives the app DATABASE_URL, which apps.api.uses[0] (postgres) already sets") {
				t.Errorf("message does not name both declarations: %v", err)
			}
		})
	}

	if _, err := loadWith(t, head+pooler); err != nil {
		t.Errorf("PgBouncer rewriting its own service was refused: %v", err)
	}
}

// The app's own listen and health ports are claimed before any sidecar's,
// so the sidecar that would take one is reported.
func TestSidecarPortsAvoidTheAppsPorts(t *testing.T) {
	const head = `version: 1
name: lab
services:
  postgres:
    type: postgres
  consul:
    type: consul
apps:
  web:
    path: ./web
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  api:
    path: ./api
    uses: [postgres, consul]
`

	for name, c := range map[string]struct{ app, path, msg string }{
		"register.port vs pgbouncer": {
			"    register: {port: 6432}\n    sidecars:\n      - type: pgbouncer\n        service: postgres\n",
			"apps.api.sidecars[0].port", "port 6432 is already used in this pod by the app, declared at apps.api.register.port",
		},
		"health.port vs outbound": {
			"    health: {port: 9001}\n    sidecars:\n      - type: envoy\n        outbound:\n          - service: web\n            port: 9001\n",
			"apps.api.sidecars[0].outbound[0].port", "declared at apps.api.health.port",
		},
		"health.port vs admin": {
			"    health: {port: 15000}\n    sidecars:\n      - type: envoy\n        outbound:\n          - service: web\n            port: 9001\n",
			"apps.api.sidecars[0]", "port 15000 is already used in this pod by the app",
		},
		"default health port vs pgbouncer": {
			"    health: {}\n    sidecars:\n      - type: pgbouncer\n        service: postgres\n        port: 8080\n",
			"apps.api.sidecars[0].port", "declared at apps.api.health",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadWith(t, head+c.app)
			if got := paths(t, err); !slices.Equal(got, []string{c.path}) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("violations = %v (%v)", got, err)
			}
		})
	}

	for name, app := range map[string]string{
		// One listener, declared twice.
		"health.port is inbound.app_port": "    health: {port: 8080}\n    sidecars:\n      - type: envoy\n        inbound: {port: 8443, app_port: 8080}\n",
		// register names Envoy's TLS listener, which the registry checks.
		"register.port is inbound.port": "    register: {port: 8443}\n    sidecars:\n      - type: envoy\n        inbound: {port: 8443, app_port: 8080}\n",
		"distinct ports":                "    health: {port: 8081}\n    register: {port: 9443}\n    sidecars:\n      - type: pgbouncer\n        service: postgres\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadWith(t, head+app); err != nil {
				t.Errorf("valid combination refused: %v", err)
			}
		})
	}
}
