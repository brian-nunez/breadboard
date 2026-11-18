package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/breadboard/internal/spec"
)

// PgBouncer infra: a connection pool shared by every app that uses it, in
// front of one Postgres service.
const (
	PgBouncerPort = 6432
	// PgBouncerAuthFile is written at container start from the
	// environment (BB_PG_USER, BB_PG_PASSWORD), so the password never
	// lands in a generated file. Explorers read the admin console as that
	// user through docker exec, like the PgBouncer sidecar's.
	PgBouncerAuthFile    = "/tmp/pgbouncer/userlist.txt"
	pgbouncerConfigFile  = "pgbouncer.ini"
	pgbouncerHBAFile     = "pgbouncer_hba.conf"
	pgbouncerDefaultMode = "transaction"
	pgbouncerDefaultSize = "10"
)

var pgbouncerPoolModes = []string{"session", "transaction", "statement"}

func pgbouncer() *Entry {
	return &Entry{
		ID:      "pgbouncer",
		Title:   "PgBouncer",
		Summary: "PgBouncer connection pool shared by apps, in front of one Postgres: TLS from apps (6432) and verify-full to Postgres.",
		Image:   "edoburu/pgbouncer:v1.26.1-p0",
		Listeners: []Listener{
			{Name: "postgres", Protocol: "postgres", Port: PgBouncerPort},
		},
		Resources: Resources{Memory: "64m", CPUs: "0.25"},
		Config: []ConfigOption{
			{Key: "pool_mode", Default: pgbouncerDefaultMode, Help: "when a server connection returns to the pool: session, transaction or statement", Validate: func(v string) error {
				if !slices.Contains(pgbouncerPoolModes, v) {
					return fmt.Errorf("must be one of %s", strings.Join(pgbouncerPoolModes, ", "))
				}

				return nil
			}},
			{Key: "pool_size", Default: pgbouncerDefaultSize, Help: "server connections per database and user", Validate: func(v string) error {
				if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 1000 {
					return errors.New("must be a whole number between 1 and 1000")
				}

				return nil
			}},
		},
		Notes: notes("pgbouncer"),
		Proxy: &Proxy{
			Modes:        []string{spec.TLSOn, spec.TLSOff},
			Targets:      []string{"postgres"},
			MinUpstreams: 1,
			MaxUpstreams: 1,
			ConfigFile:   pgbouncerConfigFile,
		},
		Server: func(s Server) Container {
			up := pgbouncerUpstream(s.Upstreams)
			db, user, pw := pgbouncerCreds(up)
			start := strings.Join([]string{
				"set -eu",
				"umask 077",
				"mkdir -p " + PgBouncerAuthFile[:strings.LastIndex(PgBouncerAuthFile, "/")],
				`printf '"%s" "%s"\n' "$$BB_PG_USER" "$$BB_PG_PASSWORD" > ` + PgBouncerAuthFile,
				"exec pgbouncer " + ConfigDir + "/" + pgbouncerConfigFile,
			}, "\n")

			return Container{
				Entrypoint: []string{"/bin/sh", "-c", start},
				Environment: []EnvVar{
					{Name: "BB_PG_USER", Value: user},
					{Name: "BB_PG_PASSWORD", Value: pw, Secret: true},
				},
				Files: []File{
					{Name: pgbouncerConfigFile, Data: pgbouncerConfig(s, up, db, user)},
					{Name: pgbouncerHBAFile, Data: pgbouncerHBA()},
				},
				Health: Health{
					// The pool answers on its public name with TLS verified
					// against the lab CA (verify-full checks the name).
					Test:          []string{"CMD", "pg_isready", "-q", "-d", fmt.Sprintf("host=%s port=%d sslmode=verify-full sslrootcert=%s", s.FQDN, PgBouncerPort, CAFile)},
					Interval:      10 * time.Second,
					Timeout:       3 * time.Second,
					Retries:       3,
					StartPeriod:   20 * time.Second,
					StartInterval: 1 * time.Second,
				},
			}
		},
		// The Postgres contract, pointing at the pool. The credentials are
		// the Postgres service's application role: every app of that
		// Postgres logs in as it, and PgBouncer reuses them for its server
		// connections.
		Client: func(c Client) []EnvVar {
			up := pgbouncerUpstream(c.Upstreams)
			db, user, pw := pgbouncerCreds(up)

			const mode = "verify-full" // TLS, and the pool's name checked

			return []EnvVar{
				{Name: "DATABASE_URL", Value: fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s", user, pw, c.FQDN, PgBouncerPort, db, mode), Secret: true},
				{Name: "PGHOST", Value: c.FQDN},
				{Name: "PGPORT", Value: strconv.Itoa(PgBouncerPort)},
				{Name: "PGUSER", Value: user},
				{Name: "PGPASSWORD", Value: pw, Secret: true},
				{Name: "PGDATABASE", Value: db},
				{Name: "PGSSLMODE", Value: mode},
				{Name: "PGSSLROOTCERT", Value: CAFile},
			}
		},
	}
}

// pgbouncerUpstream is the pool's one Postgres upstream. Validation
// requires exactly one (Proxy.MinUpstreams, MaxUpstreams); the zero
// Upstream stands in only for a spec that skipped validation.
func pgbouncerUpstream(ups []Upstream) Upstream {
	if len(ups) == 0 {
		return Upstream{}
	}

	return ups[0]
}

// pgbouncerCreds are the Postgres upstream's database, application role
// and a reference to its password.
func pgbouncerCreds(u Upstream) (db, user, password string) {
	if u.Secret != nil {
		password = u.Secret("password")
	}

	return u.Config["database"], u.Config["user"], password
}

// pgbouncerConfig renders a readable, commented pgbouncer.ini that
// learners can copy into their own file.
func pgbouncerConfig(s Server, up Upstream, db, user string) []byte {
	var b strings.Builder

	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("; pgbouncer.ini for %s, generated by Breadboard.", s.Name)
	w(";")
	w("; Apps connect with TLS to %s:%d (client_tls_sslmode = require) and", s.FQDN, PgBouncerPort)
	w("; log in as %s, the application role of %s. PgBouncer keeps a pool of", user, up.Service)
	w("; server connections to %s and lends them out (pool_mode).", up.Service)
	w(";")
	w("; To own this config, copy it next to experiment.yaml and set")
	w("; services.%s.file. The certificate (%s, %s), the lab", s.Name, CertFile, KeyFile)
	w("; CA (%s), the HBA file (%s/%s) and the auth file", CAFile, ConfigDir, pgbouncerHBAFile)
	w("; (%s, written at start from BB_PG_USER and BB_PG_PASSWORD)", PgBouncerAuthFile)
	w("; stay in place. No password appears in this file.")
	w("")
	w("[databases]")
	w("; %s", pgbouncerModeComment(up))
	w("%s = host=%s port=%d dbname=%s", db, up.Host, up.Port, db)
	w("")
	w("[pgbouncer]")
	w("listen_addr = *")
	w("listen_port = %d", PgBouncerPort)
	w("; No Unix socket: every client, the admin console included, uses TCP and TLS.")
	w("unix_socket_dir =")
	w("")
	w("; Clients: TLS required, with this service's certificate.")
	w("client_tls_sslmode = require")
	w("client_tls_cert_file = %s", CertFile)
	w("client_tls_key_file = %s", KeyFile)
	w("client_tls_protocols = tlsv1.2,tlsv1.3")
	w("")
	w("; Who may log in, and from where: see %s.", pgbouncerHBAFile)
	w("auth_type = hba")
	w("auth_hba_file = %s/%s", ConfigDir, pgbouncerHBAFile)
	w("auth_file = %s", PgBouncerAuthFile)
	w("")
	w("; Server connections to Postgres.")

	if up.Mode == spec.TLSOff {
		w("%s", Comment("; ", "Plaintext on the private segment. Reason: "+up.Reason))
		w("server_tls_sslmode = disable")
	} else {
		w("; TLS, verifying the certificate and the host name against the lab CA.")
		w("server_tls_sslmode = verify-full")
		w("server_tls_ca_file = %s", CAFile)
		w("server_tls_protocols = tlsv1.2,tlsv1.3")
	}

	w("")
	w("; transaction: a server connection is lent for one transaction, then")
	w("; returned. session: for the whole client session. statement: per statement.")
	w("pool_mode = %s", s.Config["pool_mode"])
	w("; Server connections per database and user. Every app logs in as the same")
	w("; role, so they all share this one pool.")
	w("default_pool_size = %s", s.Config["pool_size"])
	w("max_client_conn = 200")
	w("; Drivers send this; PgBouncer would otherwise refuse the connection.")
	w("ignore_startup_parameters = extra_float_digits")
	w("")
	w("; The admin console (database pgbouncer): SHOW POOLS, SHOW STATS. The HBA")
	w("; file admits it from 127.0.0.1 only, so only docker exec reaches it.")
	w("admin_users = %s", user)
	w("stats_users = %s", user)
	w("log_connections = 1")
	w("log_disconnections = 1")

	return []byte(b.String())
}

func pgbouncerModeComment(u Upstream) string {
	if u.Mode == spec.TLSOff {
		return fmt.Sprintf("%s over its private segment (plaintext; see server_tls_sslmode).", u.Service)
	}

	return fmt.Sprintf("%s over TLS, verified as %s (server_tls_sslmode).", u.Service, u.ServerName)
}

// pgbouncerHBA admits the admin console from loopback only, and everything
// else over TLS with SCRAM. First match wins.
func pgbouncerHBA() []byte {
	return []byte(strings.Join([]string{
		"# Generated by Breadboard. First matching line wins.",
		"# TYPE   DATABASE  USER ADDRESS       METHOD",
		"# The admin console: from inside the container only (bb explore, docker exec).",
		"hostssl  pgbouncer all  127.0.0.1/32  scram-sha-256",
		"host     pgbouncer all  all           reject",
		"# Apps: TLS (client_tls_sslmode = require) and SCRAM.",
		"hostssl  all       all  all           scram-sha-256",
	}, "\n") + "\n")
}
