package compose

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// TestNginxAllModesGolden renders proxyLab with NGINX as the proxy: every
// upstream mode, replicas behind re-encrypt and terminate.
func TestNginxAllModesGolden(t *testing.T) {
	s := proxyLab()
	s.Services[1].Type = "nginx"
	b := renderWith(t, s, secrets.Inline, nil)
	checkCompose(t, b)
	golden(t, "nginx", b)

	f, ok := b.File("config/edge/nginx.conf")
	if !ok || !strings.Contains(string(f.Data), "server web.bb.internal:8443 resolve") {
		t.Errorf("nginx.conf = %s", f.Data)
	}
}

// pgbouncerTerminateReason is a multi-line reason shaped like pg_hba.conf
// records: one that would admit plaintext from anywhere without a
// password, and a last line ending in a backslash, which pg_hba.conf reads
// as a continuation onto the next line.
const pgbouncerTerminateReason = "watch the server protocol in plaintext\nhostnossl all all all trust\r\nhost all all 0.0.0.0/0 trust\rlocal all all trust \\"

// pgbouncerLab has one Postgres behind two pools, one in each mode, and
// two apps sharing the TLS pool.
func pgbouncerLab() *spec.Spec {
	return &spec.Spec{
		Version: 1, Name: "lab",
		Services: []spec.Service{
			{Name: "postgres", Type: "postgres"},
			{Name: "pool", Type: "pgbouncer", Config: map[string]string{"pool_size": "5"}, Upstreams: []spec.Upstream{{Service: "postgres"}}},
			{Name: "pool-plain", Type: "pgbouncer", Upstreams: []spec.Upstream{
				{Service: "postgres", TLS: spec.TLS{Mode: spec.TLSOff, Reason: pgbouncerTerminateReason}},
			}},
		},
		Apps: []spec.App{
			{Name: "orders", Path: "./orders", Uses: []spec.Use{{Service: "pool"}}},
			{Name: "billing", Path: "./billing", Uses: []spec.Use{{Service: "pool"}}},
			{Name: "audit", Path: "./audit", Uses: []spec.Use{{Service: "pool-plain"}}},
		},
	}
}

func TestPgBouncerGolden(t *testing.T) {
	b := renderWith(t, pgbouncerLab(), secrets.Dotenv, nil)
	checkCompose(t, b)
	golden(t, "pgbouncer-infra", b)
}

// Apps reach the pool over TLS with the Postgres app role; the password
// is only ever a reference, and the plaintext pool joins Postgres's
// segment while the apps stay off it.
func TestPgBouncerPoolWiring(t *testing.T) {
	b := renderWith(t, pgbouncerLab(), secrets.Dotenv, nil)
	f, _ := b.File(ComposeFile)

	var doc struct {
		Services map[string]struct {
			Environment map[string]string         `yaml:"environment"`
			Networks    map[string]map[string]any `yaml:"networks"`
			Volumes     []string                  `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(f.Data, &doc); err != nil {
		t.Fatal(err)
	}

	pw := "${" + secrets.VarName("postgres", "password") + "}"

	for _, app := range []string{"orders", "billing"} {
		env := doc.Services[app].Environment
		if env["DATABASE_URL"] != "postgres://app:"+pw+"@pool.bb.internal:6432/app?sslmode=verify-full" ||
			env["PGHOST"] != "pool.bb.internal" || env["PGPORT"] != "6432" || env["PGSSLMODE"] != "verify-full" || env["PGPASSWORD"] != pw {
			t.Errorf("%s env = %v", app, env)
		}
	}

	pool := doc.Services["pool"]
	if pool.Environment["BB_PG_USER"] != "app" || pool.Environment["BB_PG_PASSWORD"] != pw {
		t.Errorf("pool env = %v", pool.Environment)
	}

	for _, file := range b.Files {
		if strings.HasPrefix(file.Path, "config/pool") && strings.Contains(string(file.Data), "PASSWORD") && !strings.Contains(string(file.Data), "BB_PG_PASSWORD") {
			t.Errorf("%s mentions a password:\n%s", file.Path, file.Data)
		}
	}

	for name, s := range doc.Services {
		_, onSeg := s.Networks["plain-postgres"]
		if want := name == "postgres" || name == "pool-plain"; onSeg != want {
			t.Errorf("%s on plain-postgres = %v, want %v", name, onSeg, want)
		}
	}

	if _, ok := doc.Volumes["pool-data"]; ok {
		t.Error("the pool has a data volume")
	}

	var hba string
	if f, ok := b.File("config/postgres/pg_hba.conf"); ok {
		hba = string(f.Data)
	}

	if !strings.Contains(hba, "hostnossl all      all  10.213.") || !strings.Contains(hba, "pool-plain") {
		t.Errorf("pg_hba.conf does not admit the plaintext pool's segment:\n%s", hba)
	}

	if !slices.ContainsFunc(b.Files, func(f File) bool { return f.Path == "config/pool/pgbouncer_hba.conf" }) {
		t.Error("no pgbouncer_hba.conf")
	}
}

// A terminate reason reaches Postgres's pg_hba.conf through the PgBouncer
// upstream (review B1). Read as Postgres reads it (a trailing backslash
// continues a line, then "#" starts a comment), the file holds exactly the
// generated records: the reason added none and swallowed none.
func TestTerminateReasonInjectsNoHBARecord(t *testing.T) {
	b := renderWith(t, pgbouncerLab(), secrets.Dotenv, nil)

	f, ok := b.File("config/postgres/pg_hba.conf")
	if !ok {
		t.Fatal("no pg_hba.conf")
	}

	records := hbaRecords(string(f.Data))

	want := []string{
		"local all all trust",
		"hostssl all all all scram-sha-256",
		"hostnossl all all 10.213.0.0/24 scram-sha-256",
	}
	if !slices.Equal(records, want) {
		t.Errorf("pg_hba.conf records = %q, want %q:\n%s", records, want, f.Data)
	}
	// The reason is still there for a reader, one comment line per line.
	for _, line := range []string{"# Reason: pool-plain: watch the server protocol in plaintext\n", "# hostnossl all all all trust\n", "# host all all 0.0.0.0/0 trust\n", "# local all all trust \\#\n"} {
		if !strings.Contains(string(f.Data), line) {
			t.Errorf("pg_hba.conf lacks %q:\n%s", line, f.Data)
		}
	}
}

// hbaRecords returns pg_hba.conf's records as Postgres's tokenizer sees
// them, fields joined by one space.
func hbaRecords(conf string) []string {
	var out []string

	lines := strings.Split(conf, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, `\`) + strings.TrimSuffix(lines[i], "\r")
		}

		inQuote := false

		for j, r := range line {
			if r == '"' {
				inQuote = !inQuote
			}

			if r == '#' && !inQuote {
				line = line[:j]

				break
			}
		}

		if fields := strings.Fields(line); len(fields) > 0 {
			out = append(out, strings.Join(fields, " "))
		}
	}

	return out
}
