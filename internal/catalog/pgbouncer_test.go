package catalog

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// pgbouncerServer is a pool in front of one Postgres, as compose renders it.
func pgbouncerServer(mode string) Server {
	e, _ := Lookup("pgbouncer")
	pg, _ := Lookup("postgres")
	up := Upstream{Name: "postgres", Service: "postgres", Mode: mode, Host: "postgres.bb.internal", ServerName: "postgres.bb.internal", Port: 5432,
		Config: pg.ResolveConfig(nil), Secret: func(k string) string { return "${POSTGRES_" + strings.ToUpper(k) + "}" }}
	if mode == spec.TLSOff {
		up.Reason, up.Host = "practice", "postgres.plain.bb.internal"
	}
	up.Servers = []string{up.Host}
	return Server{Name: "pool", FQDN: "pool.bb.internal", Domain: "bb.internal", Config: e.ResolveConfig(nil), Upstreams: []Upstream{up}}
}

func TestPgBouncerConfigShapes(t *testing.T) {
	e, _ := Lookup("pgbouncer")
	c := e.Server(pgbouncerServer(spec.TLSOn))
	var ini, hba string
	for _, f := range c.Files {
		switch f.Name {
		case "pgbouncer.ini":
			ini = string(f.Data)
		case "pgbouncer_hba.conf":
			hba = string(f.Data)
		}
	}
	for _, want := range []string{
		"app = host=postgres.bb.internal port=5432 dbname=app",
		"listen_port = 6432",
		"client_tls_sslmode = require",
		"client_tls_cert_file = " + CertFile,
		"client_tls_key_file = " + KeyFile,
		"auth_type = hba",
		"auth_file = " + PgBouncerAuthFile,
		"server_tls_sslmode = verify-full",
		"server_tls_ca_file = " + CAFile,
		"pool_mode = transaction",
		"default_pool_size = 10",
		"admin_users = app",
	} {
		if !strings.Contains(ini, want) {
			t.Errorf("pgbouncer.ini lacks %q:\n%s", want, ini)
		}
	}
	if !strings.Contains(hba, "hostssl  pgbouncer all  127.0.0.1/32") || !strings.Contains(hba, "host     pgbouncer all  all           reject") {
		t.Errorf("the admin console is not loopback only:\n%s", hba)
	}
	// The password reaches the container as a reference in its env only.
	for _, f := range c.Files {
		if strings.Contains(string(f.Data), "${POSTGRES_PASSWORD}") {
			t.Errorf("%s holds the password", f.Name)
		}
	}
	if len(c.Environment) != 2 || c.Environment[1].Name != "BB_PG_PASSWORD" || c.Environment[1].Value != "${POSTGRES_PASSWORD}" || !c.Environment[1].Secret {
		t.Errorf("env = %+v", c.Environment)
	}
	if h := strings.Join(c.Health.Test, " "); !strings.Contains(h, "pg_isready") || !strings.Contains(h, "sslmode=verify-full") {
		t.Errorf("health check = %v", c.Health.Test)
	}

	// Terminate: plaintext over the Postgres segment.
	off := string(e.Server(pgbouncerServer(spec.TLSOff)).Files[0].Data)
	if !strings.Contains(off, "host=postgres.plain.bb.internal") || !strings.Contains(off, "server_tls_sslmode = disable") || strings.Contains(off, "server_tls_ca_file") {
		t.Errorf("terminate config:\n%s", off)
	}
	// Config is honoured.
	s := pgbouncerServer(spec.TLSOn)
	s.Config = e.ResolveConfig(map[string]string{"pool_mode": "session", "pool_size": "3"})
	if ini := string(e.Server(s).Files[0].Data); !strings.Contains(ini, "pool_mode = session") || !strings.Contains(ini, "default_pool_size = 3") {
		t.Errorf("config not applied:\n%s", ini)
	}
	for k, v := range map[string]string{"pool_mode": "batch", "pool_size": "0"} {
		if err := e.CheckConfig(k, v); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
}

func TestPgBouncerClientEnv(t *testing.T) {
	e, _ := Lookup("pgbouncer")
	env := map[string]string{}
	for _, v := range e.Client(Client{Service: "pool", FQDN: "pool.bb.internal", Upstreams: pgbouncerServer(spec.TLSOn).Upstreams}) {
		env[v.Name] = v.Value
	}
	want := map[string]string{
		"DATABASE_URL":  "postgres://app:${POSTGRES_PASSWORD}@pool.bb.internal:6432/app?sslmode=verify-full",
		"PGHOST":        "pool.bb.internal",
		"PGPORT":        "6432",
		"PGUSER":        "app",
		"PGPASSWORD":    "${POSTGRES_PASSWORD}",
		"PGDATABASE":    "app",
		"PGSSLMODE":     "verify-full",
		"PGSSLROOTCERT": CAFile,
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	info, _ := Types("pgbouncer")
	if info.Proxy == nil || info.Proxy.HTTP || info.Proxy.MinUpstreams != 1 || info.Proxy.MaxUpstreams != 1 || len(info.Proxy.Targets) != 1 || info.Proxy.Targets[0] != "postgres" ||
		len(info.Proxy.Modes) != 2 || info.HTTPS || info.Plaintext {
		t.Errorf("type info = %+v", info)
	}
	if got := info.Env("pool", false); len(got) != 8 || got[0] != "DATABASE_URL" {
		t.Errorf("Env = %v", got)
	}
}

// TestPgBouncerStartsInImage starts PgBouncer in the pinned image with the
// generated config (PgBouncer has no config-check mode), then checks that
// the admin console answers over TLS on loopback and that a plaintext
// login is refused. It needs Docker.
func TestPgBouncerStartsInImage(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available")
	}
	t.Parallel()
	e, _ := Lookup("pgbouncer")
	now := time.Now()
	ca, err := pki.NewCA("bb.internal", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Issue(pki.LeafRequest{DNSNames: []string{"pool.bb.internal"}, Identity: pki.Identity("bb.internal", "lab", "pool")}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{spec.TLSOn, spec.TLSOff} {
		s := pgbouncerServer(mode)
		s.Upstreams[0].Secret = func(string) string { return "IGNORED" }
		c := e.Server(s)
		dir := t.TempDir()
		files := map[string][]byte{"tls/tls.crt": leaf.CertPEM, "tls/tls.key": leaf.KeyPEM, "ca.pem": ca.CertPEM}
		for _, f := range c.Files {
			files["config/"+f.Name] = f.Data
		}
		writeTree(t, dir, files)
		start := strings.ReplaceAll(c.Entrypoint[2], "$$", "$") // compose unescapes $$
		name := "bb-pgbouncer-check-" + mode
		_ = exec.Command("docker", "rm", "-f", name).Run()
		out, err := exec.Command("docker", "run", "-d", "--name", name, "--network", "none", "-v", dir+":/etc/bb:ro",
			"-e", "BB_PG_USER=app", "-e", "BB_PG_PASSWORD=secret-pw",
			"--entrypoint", "/bin/sh", e.Image, "-c", start).CombinedOutput()
		if err != nil {
			t.Fatalf("docker run: %v\n%s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		psql := func(sslmode string) (string, error) {
			b, err := exec.Command("docker", "exec", "-e", "PGPASSWORD=secret-pw", name, "psql", "-X", "-tA",
				"host=127.0.0.1 port=6432 dbname=pgbouncer user=app sslmode="+sslmode, "-c", "SHOW POOLS").CombinedOutput()
			return string(b), err
		}
		var res string
		deadline := time.Now().Add(20 * time.Second)
		for {
			if res, err = psql("require"); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: admin console over TLS: %v\n%s\nlogs:\n%s", mode, err, res, logs)
		}
		if res, err := psql("disable"); err == nil {
			t.Errorf("%s: a plaintext login was accepted:\n%s", mode, res)
		}
		if strings.Contains(string(logs), "secret-pw") || strings.Contains(string(logs), "WARNING") || strings.Contains(string(logs), "ERROR") {
			t.Errorf("%s: logs:\n%s", mode, logs)
		}
		t.Logf("%s: PgBouncer up; logs:\n%s", mode, logs)
	}
}

// The real entry needs exactly one postgres upstream: validation through
// the catalog reports services.pool.upstreams with none, an empty list or
// two, and the target with another type.
func TestPgBouncerNeedsOnePostgres(t *testing.T) {
	head := "version: 1\nname: lab\nservices:\n  postgres:\n    type: postgres\n  postgres2:\n    type: postgres\n  redis:\n    type: redis\n  pool:\n    type: pgbouncer\n"
	for upstreams, want := range map[string]string{
		"":                                       "services.pool.upstreams: type pgbouncer needs exactly 1 upstream (a postgres service)",
		"    upstreams: []\n":                    "services.pool.upstreams: type pgbouncer needs exactly 1 upstream (a postgres service)",
		"    upstreams: [postgres]\n":            "",
		"    upstreams: [postgres, postgres2]\n": "services.pool.upstreams: type pgbouncer takes at most 1 upstream",
		"    upstreams: [redis]\n":               "services.pool.upstreams[0].service: type pgbouncer only takes postgres services as upstreams, not redis",
	} {
		doc, err := spec.Parse([]byte(head + upstreams))
		if err != nil {
			t.Fatal(err)
		}
		s, err := doc.Decode()
		if err != nil {
			t.Fatal(err)
		}
		err = spec.Validate(s, Types)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", upstreams, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: error = %v, want %q", upstreams, err, want)
		}
	}
}
