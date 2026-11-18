package compose

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// sidecarDoc is the part of a compose file the sidecar tests read.
type sidecarDoc struct {
	Services map[string]struct {
		Image       string            `yaml:"image"`
		Entrypoint  []string          `yaml:"entrypoint"`
		Command     []string          `yaml:"command"`
		NetworkMode string            `yaml:"network_mode"`
		Volumes     []string          `yaml:"volumes"`
		CapAdd      []string          `yaml:"cap_add"`
		CapDrop     []string          `yaml:"cap_drop"`
		SecurityOpt []string          `yaml:"security_opt"`
		Restart     string            `yaml:"restart"`
		Environment map[string]string `yaml:"environment"`
		Healthcheck struct {
			Test []string `yaml:"test"`
		} `yaml:"healthcheck"`
		DependsOn map[string]struct {
			Condition string `yaml:"condition"`
		} `yaml:"depends_on"`
	} `yaml:"services"`
}

func parseCompose(t *testing.T, b *Bundle) sidecarDoc {
	t.Helper()

	f, _ := b.File(ComposeFile)

	var doc sidecarDoc
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}

	return doc
}

func renderWith(t *testing.T, s *spec.Spec, mode secrets.Mode, files map[string]string) *Bundle {
	t.Helper()

	if err := spec.Validate(s, catalog.Types); err != nil {
		t.Fatalf("spec does not validate: %v", err)
	}

	m, err := model.Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}

	return renderModelWith(t, m, mode, func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			return []byte(c), nil
		}

		return nil, fs.ErrNotExist
	})
}

// An Envoy sidecar terminates TLS in front of its app and proxies the
// app's loopback calls to an app with replicas and to an infra service.
func TestEnvoySidecarRenders(t *testing.T) {
	retries := 0
	s := &spec.Spec{
		Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "s3", Type: "s3-compatible"}},
		Apps: []spec.App{
			{Name: "web", Path: "./web", Replicas: 3, Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
				Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080},
			}}}},
			{Name: "caller", Path: "./caller", Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
				Outbound: []spec.EnvoyOutbound{
					{Service: "web", Port: 9001, LB: "least_request", Timeout: "1m30s", Outlier: &spec.Outlier{Consecutive5xx: 3, Ejection: "30s"}},
					{Service: "s3", Port: 9002, Retries: &retries},
				},
			}}}},
		},
	}
	b := renderWith(t, s, secrets.Dotenv, nil)
	checkCompose(t, b)
	doc := parseCompose(t, b)

	for _, n := range []string{"1", "2", "3"} {
		inst := "web-" + n
		if env := doc.Services[inst].Environment; env["BB_LISTEN_ADDR"] != "127.0.0.1:8080" {
			t.Errorf("%s env = %v", inst, env)
		}

		sc := doc.Services[inst+"-envoy"]
		if sc.Image != "envoyproxy/envoy:v1.40.2" || sc.NetworkMode != "service:"+inst+"-pod" || len(sc.CapAdd) > 0 {
			t.Errorf("%s-envoy = %+v", inst, sc)
		}

		if !strings.Contains(strings.Join(sc.Healthcheck.Test, " "), "/dev/tcp/127.0.0.1/15000") {
			t.Errorf("%s-envoy health = %v", inst, sc.Healthcheck.Test)
		}
	}

	caller := doc.Services["caller"]
	if caller.Environment["WEB_URL"] != "http://127.0.0.1:9001" || caller.Environment["S3_URL"] != "http://127.0.0.1:9002" {
		t.Errorf("caller env = %v", caller.Environment)
	}

	if _, ok := caller.Environment["BB_LISTEN_ADDR"]; ok {
		t.Error("an Envoy without inbound set BB_LISTEN_ADDR")
	}

	if doc.Services["caller-envoy"].DependsOn["s3"].Condition != "service_healthy" {
		t.Error("the Envoy sidecar does not wait for its infra target")
	}

	if _, ok := doc.Services["caller-envoy"].DependsOn["web-1"]; ok {
		t.Error("the Envoy sidecar waits for an app target (it re-resolves instead)")
	}

	golden(t, "envoy", b)
}

// Transparent mode adds an init container that is the only holder of
// NET_ADMIN, built from a generated Dockerfile, which exempts a
// registration agent in the same pod.
func TestTransparentEnvoyRenders(t *testing.T) {
	s := &spec.Spec{
		Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "postgres", Type: "postgres"}, {Name: "consul", Type: "consul"}},
		Apps: []spec.App{
			{Name: "web", Path: "./web", Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
				Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080},
			}}}},
			{
				Name: "caller", Path: "./caller", Uses: uses("postgres", "consul"), Register: &spec.Register{Port: 8080},
				Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
					Outbound:    []spec.EnvoyOutbound{{Service: "web", Port: 9001}},
					Transparent: &spec.Transparent{Egress: spec.EgressDeny},
				}}},
			},
		},
	}
	b := renderWith(t, s, secrets.Dotenv, nil)
	checkCompose(t, b)
	doc := parseCompose(t, b)

	init := doc.Services["caller-netinit"]
	if !slices.Equal(init.CapAdd, []string{"NET_ADMIN"}) || init.Restart != "no" || init.NetworkMode != "service:caller-pod" {
		t.Errorf("netinit = %+v", init)
	}

	for name, svc := range doc.Services {
		if name != "caller-netinit" && len(svc.CapAdd) > 0 {
			t.Errorf("%s holds capabilities %v", name, svc.CapAdd)
		}
	}

	script := strings.Join(init.Command, " ")
	for _, want := range []string{
		"--uid-owner 1337 -j RETURN", "--uid-owner 1338 -j RETURN", "-d 127.0.0.0/8 -j RETURN",
		"REDIRECT --to-ports 15001", "--dport 8301 -j RETURN", "--dport 8080 -j REDIRECT --to-ports 15006",
		"-A BB_IN -p tcp -j REJECT --reject-with tcp-reset", "iptables -t nat -S", "iptables -t filter -S",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("netinit script lacks %q:\n%s", want, script)
		}
	}

	if strings.Contains(script, "--uid-owner 101") || strings.Contains(script, "--uid-owner 100 ") {
		t.Errorf("netinit exempts an image's default user:\n%s", script)
	}

	if env := doc.Services["caller-envoy"].Environment; env["ENVOY_UID"] != "1337" || env["ENVOY_GID"] != "1337" {
		t.Errorf("caller-envoy env = %v", env)
	}

	if env := doc.Services["caller-agent"].Environment; env["CONSUL_UID"] != "1338" {
		t.Errorf("caller-agent env = %v", env)
	}

	if _, ok := doc.Services["web-envoy"].Environment["ENVOY_UID"]; ok {
		t.Error("an Envoy outside a transparent pod changed its user")
	}

	if app := doc.Services["caller"]; !slices.Equal(app.SecurityOpt, []string{"no-new-privileges:true"}) || !slices.Equal(app.CapDrop, []string{"SETUID", "SETGID"}) {
		t.Errorf("transparent app security = %v %v", app.SecurityOpt, app.CapDrop)
	}

	if web := doc.Services["web"]; web.SecurityOpt != nil || web.CapDrop != nil {
		t.Errorf("a non-transparent app was restricted: %v %v", web.SecurityOpt, web.CapDrop)
	}

	cfg, _ := b.File("config/caller-envoy/envoy.yaml")
	for _, want := range []string{"address_prefix: 10.213.200.12", "address_prefix: 10.213.200.10", "address_prefix: 10.213.200.11", "name: uses_5432", "name: transparent_tls_8443", "name: app_tls_8080"} {
		if !strings.Contains(string(cfg.Data), want) {
			t.Errorf("caller-envoy config lacks %q", want)
		}
	}

	df, ok := b.File("build/caller-netinit/Dockerfile")
	if !ok || !strings.Contains(string(df.Data), "FROM alpine:3.24.4\nRUN apk add --no-cache iptables\n") {
		t.Errorf("netinit Dockerfile = %q", df.Data)
	}

	for _, sc := range []string{"caller-envoy", "caller-agent", "caller"} {
		if doc.Services[sc].DependsOn["caller-netinit"].Condition != "service_completed_successfully" {
			t.Errorf("%s does not wait for the rules", sc)
		}
	}

	golden(t, "transparent", b)
}

// PgBouncer pools the app's connections; the app's Postgres variables
// point at it, and no secret value lands in a generated file.
func TestPgBouncerSidecarRenders(t *testing.T) {
	s := &spec.Spec{
		Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "postgres", Type: "postgres", Config: map[string]string{"database": "orders"}}, {Name: "reports", Type: "postgres"}},
		Apps: []spec.App{{
			Name: "api", Path: "./api", Uses: uses("postgres", "reports"),
			Sidecars: []spec.Sidecar{{Type: "pgbouncer", PgBouncer: &spec.PgBouncerSidecar{Service: "reports", PoolMode: "session", Port: 7432}}},
		}},
	}
	b := renderWith(t, s, secrets.Dotenv, nil)
	checkCompose(t, b)
	doc := parseCompose(t, b)

	env := doc.Services["api"].Environment
	if env["REPORTS_DATABASE_URL"] != "postgres://app:${REPORTS__PASSWORD}@127.0.0.1:7432/app?sslmode=disable" ||
		env["REPORTS_PGHOST"] != "127.0.0.1" || env["REPORTS_PGPORT"] != "7432" || env["REPORTS_PGSSLMODE"] != "disable" {
		t.Errorf("pooled service env = %v", env)
	}

	if env["DATABASE_URL"] != "postgres://app:${POSTGRES__PASSWORD}@postgres.bb.internal:5432/orders?sslmode=verify-full" {
		t.Errorf("the other Postgres service was rewritten: %s", env["DATABASE_URL"])
	}

	pb := doc.Services["api-pgbouncer"]
	if pb.Environment["BB_PG_PASSWORD"] != "${REPORTS__PASSWORD}" || pb.DependsOn["reports"].Condition != "service_healthy" {
		t.Errorf("pgbouncer = %+v", pb)
	}

	if got := strings.Join(pb.Healthcheck.Test, " "); got != "CMD pg_isready -q -h 127.0.0.1 -p 7432" {
		t.Errorf("health = %s", got)
	}

	golden(t, "pgbouncer", b)
}

// Fluent Bit ships the shared log directory to a bucket, which the S3
// compatible service is told to create.
func TestFluentBitSidecarRenders(t *testing.T) {
	s := &spec.Spec{
		Version: 1, Name: "lab",
		Services: []spec.Service{{Name: "s3", Type: "s3-compatible", Config: map[string]string{"buckets": "uploads"}}},
		Apps: []spec.App{{
			Name: "api", Path: "./api", Uses: uses("s3"),
			Sidecars: []spec.Sidecar{{Type: "fluent-bit", FluentBit: &spec.FluentBitSidecar{To: "s3", Bucket: "logs"}}},
		}},
	}
	b := renderWith(t, s, secrets.Dotenv, nil)
	checkCompose(t, b)

	doc := parseCompose(t, b)
	if doc.Services["s3"].Environment["BB_BUCKETS"] != "uploads,logs" {
		t.Errorf("buckets = %q", doc.Services["s3"].Environment["BB_BUCKETS"])
	}

	if doc.Services["api"].Environment["BB_LOG_DIR"] != "/var/run/bb/logs" {
		t.Errorf("app env = %v", doc.Services["api"].Environment)
	}

	fb := doc.Services["api-fluent-bit"]
	if fb.Environment["AWS_ACCESS_KEY_ID"] != "${S3__ACCESS_KEY_ID}" || !slices.Contains(fb.Volumes, "api-shared:/var/run/bb") {
		t.Errorf("fluent-bit = %+v", fb)
	}

	golden(t, "fluent-bit", b)
}

// file: replaces the generated Envoy config with the user's, copied into
// the bundle; certificates stay mounted at the usual paths.
func TestEnvoyUserFileReplacesTheConfig(t *testing.T) {
	s := &spec.Spec{
		Version: 1, Name: "lab",
		Apps: []spec.App{{Name: "web", Path: "./web", Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{
			Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080}, File: "envoy/web.yaml",
		}}}}},
	}
	b := renderWith(t, s, secrets.Dotenv, map[string]string{"envoy/web.yaml": "# mine\n"})

	cfg, ok := b.File("config/web-envoy/envoy.yaml")
	if !ok || string(cfg.Data) != "# mine\n" {
		t.Fatalf("envoy.yaml = %q", cfg.Data)
	}

	doc := parseCompose(t, b)

	vols := doc.Services["web-envoy"].Volumes
	for _, want := range []string{"./config/web-envoy/envoy.yaml:/etc/bb/config/envoy.yaml:ro", "./tls/web/tls.crt:/etc/bb/tls/tls.crt:ro", "./ca.pem:/etc/bb/ca.pem:ro"} {
		if !slices.Contains(vols, want) {
			t.Errorf("volumes lack %s: %v", want, vols)
		}
	}

	m, _ := model.Resolve(s, "bb.internal")
	if _, err := renderModelErr(m, secrets.Dotenv, func(string) ([]byte, error) { return nil, fs.ErrNotExist }); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "web-envoy") {
		t.Errorf("missing file: err = %v", err)
	}
}

// Env ownership: a sidecar may replace only the variables of the service
// it rewrites (PgBouncer's own Postgres); any other collision fails the
// render, whatever the sidecar order.
func TestSidecarEnvOwnership(t *testing.T) {
	envoy := spec.Sidecar{Type: "envoy", Envoy: &spec.EnvoySidecar{Outbound: []spec.EnvoyOutbound{{Service: "database", Port: 9001}}}}
	pooler := spec.Sidecar{Type: "pgbouncer", PgBouncer: &spec.PgBouncerSidecar{Service: "postgres"}}

	lab := func(sidecars ...spec.Sidecar) *spec.Spec {
		return &spec.Spec{
			Version: 1, Name: "lab",
			Services: []spec.Service{{Name: "postgres", Type: "postgres"}},
			Apps: []spec.App{
				{Name: "database", Path: "./db", Sidecars: []spec.Sidecar{{Type: "envoy", Envoy: &spec.EnvoySidecar{Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080}}}}},
				{Name: "api", Path: "./api", Uses: uses("postgres"), Sidecars: sidecars},
			},
		}
	}
	for name, sidecars := range map[string][]spec.Sidecar{
		"envoy only":           {envoy},
		"envoy then pgbouncer": {envoy, pooler},
		"pgbouncer then envoy": {pooler, envoy},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := model.Resolve(lab(sidecars...), "bb.internal") // validation would refuse it
			if err != nil {
				t.Fatal(err)
			}

			if _, err := renderModelErr(m, secrets.Dotenv, nil); err == nil || !strings.Contains(err.Error(), "sets DATABASE_URL, which") {
				t.Errorf("err = %v, want a DATABASE_URL collision", err)
			}
		})
	}

	b := renderWith(t, lab(pooler), secrets.Dotenv, nil)
	if got := parseCompose(t, b).Services["api"].Environment["DATABASE_URL"]; !strings.Contains(got, "@127.0.0.1:6432/") {
		t.Errorf("PgBouncer did not rewrite its own service's DATABASE_URL: %s", got)
	}
}

// A pod's capture mode is a label on its holder and every container that
// joins it, so switching transparent mode on or off (or removing the Envoy
// sidecar) changes each of their definitions and compose recreates the
// namespace instead of leaving stale traffic rules in it.
func TestCaptureModeLabelsThePod(t *testing.T) {
	lab := func(sidecars ...spec.Sidecar) *spec.Spec {
		return &spec.Spec{
			Version: 1, Name: "lab",
			Services: []spec.Service{{Name: "postgres", Type: "postgres"}},
			Apps:     []spec.App{{Name: "api", Path: "./api", Uses: uses("postgres"), Sidecars: sidecars}},
		}
	}
	pooler := spec.Sidecar{Type: "pgbouncer", PgBouncer: &spec.PgBouncerSidecar{Service: "postgres"}}
	transparent := spec.Sidecar{Type: "envoy", Envoy: &spec.EnvoySidecar{Transparent: &spec.Transparent{}}}
	labels := func(s *spec.Spec) map[string]string {
		f, _ := renderWith(t, s, secrets.Dotenv, nil).File(ComposeFile)

		var doc struct {
			Services map[string]struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(f.Data, &doc); err != nil {
			t.Fatal(err)
		}

		out := map[string]string{}

		for name, svc := range doc.Services {
			if v, ok := svc.Labels[LabelCapture]; ok {
				out[name] = v
			}
		}

		return out
	}

	on := labels(lab(pooler, transparent))
	for _, name := range []string{"api-pod", "api", "api-pgbouncer", "api-envoy"} {
		if on[name] != "transparent" {
			t.Errorf("with transparent mode, %s capture = %q", name, on[name])
		}
	}

	off := labels(lab(pooler))
	for _, name := range []string{"api-pod", "api", "api-pgbouncer"} {
		if off[name] != "off" {
			t.Errorf("without Envoy, %s capture = %q", name, off[name])
		}
	}

	if _, ok := off["postgres"]; ok {
		t.Error("a service outside any pod has a capture label")
	}
}
