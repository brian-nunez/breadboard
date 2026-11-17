//go:build integration

package core

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestIntegrationNginx runs NGINX with one upstream in each TLS mode and
// checks what each mode promises:
//
//   - re-encrypt: the client verifies NGINX's certificate, and requests
//     reach more than one of the target's 3 replicas over TLS;
//   - terminate: the app's plaintext port answers NGINX on the private
//     segment, and no other container can reach it;
//   - passthrough: the client sees the backend's own certificate.
func TestIntegrationNginx(t *testing.T) {
	_, _, name := sidecarLab(t, "nginx", `services:
  edge:
    type: nginx
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
apps:
  web:
    path: ./probe
    command: [probe, servetls, ":8443"]
    replicas: 3
  api:
    path: ./probe
    command: [probe, serve]
    replicas: 2
  payments:
    path: ./probe
    command: [probe, servetls, ":8443"]
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge]
`, nil)

	t.Run("re-encrypt", func(t *testing.T) {
		out, err := execIn(t, name, "client", `test "$EDGE_URL" = https://edge.bb.internal:8443 && probe peer edge.bb.internal:8443 edge.bb.internal`)
		if err != nil || !strings.Contains(out, "peer=edge.bb.internal") {
			t.Fatalf("the client did not verify NGINX's certificate: %v\n%s", err, out)
		}
		eventually(t, time.Minute, "requests to reach more than one replica", func() bool {
			out, err = execIn(t, name, "client", `probe hits "$EDGE_URL/web/" 30`)
			if err != nil {
				t.Logf("hits: %v %s", err, out)
				return false
			}
			return strings.Count(out, "instance=web-") >= 2
		})
		t.Logf("distribution:\n%s", out)
		// The first upstream is also the default route; the backend got
		// TLS from NGINX (it serves nothing else) and the forwarded proto.
		out, err = execIn(t, name, "client", `probe get "$EDGE_URL/anything"`)
		if err != nil || !strings.Contains(out, "instance=web-") || !strings.Contains(out, "proto=https") || !strings.Contains(out, "host=edge.bb.internal") {
			t.Errorf("default route: %v\n%s", err, out)
		}
		// stub_status answers on loopback only.
		if out, err := execIn(t, name, "edge", `wget -qO- http://127.0.0.1:8404/stub_status`); err != nil || !strings.Contains(out, "Active connections:") {
			t.Errorf("stub_status: %v\n%s", err, out)
		}
		if out, err := execIn(t, name, "client", "probe dial edge.bb.internal:8404"); err == nil {
			t.Errorf("stub_status is reachable from another container:\n%s", out)
		}
	})

	t.Run("terminate", func(t *testing.T) {
		var out string
		eventually(t, time.Minute, "the terminated app to answer through NGINX", func() bool {
			var err error
			out, err = execIn(t, name, "client", `probe get "$EDGE_URL/api/"`)
			return err == nil && strings.Contains(out, "instance=api-")
		})
		addr, err := execIn(t, name, "api-1", `echo "$BB_PLAIN_LISTEN_ADDR"`)
		addr = strings.TrimSpace(addr)
		if err != nil || !strings.HasPrefix(addr, "10.213.") || !strings.HasSuffix(addr, ":8080") {
			t.Fatalf("api-1 BB_PLAIN_LISTEN_ADDR = %q (%v)", addr, err)
		}
		_, port, _ := strings.Cut(addr, ":")
		// NGINX is on the segment: plain HTTP answers.
		if out, err := execIn(t, name, "edge", "wget -qO- http://"+addr+"/"); err != nil || !strings.Contains(out, "instance=api-1") {
			t.Errorf("NGINX cannot reach the plaintext port %s: %v\n%s", addr, err, out)
		}
		// Nobody else is: not by segment address, not by the app's name.
		for _, target := range []string{addr, "api-1.bb.internal:" + port, "api.plain.bb.internal:" + port} {
			if out, err := execIn(t, name, "client", "probe dial "+target); err == nil {
				t.Errorf("another container reached the plaintext port at %s:\n%s", target, out)
			}
		}
	})

	t.Run("passthrough", func(t *testing.T) {
		var out string
		eventually(t, time.Minute, "passthrough to reach the backend", func() bool {
			var err error
			out, err = execIn(t, name, "client", `test "$EDGE_PASSTHROUGH_ADDR" = edge.bb.internal:9443 && probe peer "$EDGE_PASSTHROUGH_ADDR" payments.bb.internal`)
			return err == nil
		})
		if !strings.Contains(out, "peer=payments.bb.internal") || strings.Contains(out, "edge.bb.internal") || !strings.Contains(out, "instance=payments proto=tls") {
			t.Errorf("the client did not see the backend's own certificate:\n%s", out)
		}
		if out, err := execIn(t, name, "client", "probe peer edge.bb.internal:9443 web.bb.internal"); err == nil {
			t.Errorf("an SNI no upstream claims was routed:\n%s", out)
		}
	})
}

// TestIntegrationPgBouncerInfra: two apps share one pool in front of
// Postgres. Each logs in with TLS and verify-full to the pool; Postgres
// sees server connections only from PgBouncer, over TLS, and never more
// than pool_size of them; the admin console answers on loopback for the
// pools explorer and refuses everyone else. A second pool in terminate
// mode reaches Postgres in plaintext over its private segment.
func TestIntegrationPgBouncerInfra(t *testing.T) {
	const poolSize = 3
	_, _, name := sidecarLab(t, "pgbi", fmt.Sprintf(`services:
  postgres:
    type: postgres
  pool:
    type: pgbouncer
    config: {pool_size: %d}
    upstreams: [postgres]
  pool-plain:
    type: pgbouncer
    upstreams:
      - service: postgres
        tls:
          mode: off
          # Shaped like pg_hba.conf records (review B1); the last line ends in
          # a backslash, which pg_hba.conf reads as a continuation.
          reason: "watch the server protocol in plaintext\nhostnossl all all all trust\r\nhost all all 0.0.0.0/0 trust\rlocal all all trust \\"
apps:
  orders:
    path: ./pg
    command: [sh, -c, "trap 'exit 0' TERM; while :; do sleep 1; done"]
    uses: [pool]
  billing:
    path: ./pg
    command: [sh, -c, "trap 'exit 0' TERM; while :; do sleep 1; done"]
    uses: [pool]
`, poolSize), map[string]map[string]string{"pg": {"Dockerfile": "FROM postgres:18.7-alpine3.24 AS dev\n"}})

	for _, app := range []string{"orders", "billing"} {
		if out, err := execIn(t, name, app, `test "$PGHOST:$PGPORT:$PGSSLMODE" = pool.bb.internal:6432:verify-full && case "$DATABASE_URL" in *@pool.bb.internal:6432/app?sslmode=verify-full) ;; *) exit 1;; esac`); err != nil {
			t.Fatalf("%s's Postgres variables do not point at the pool: %v\n%s", app, err, out)
		}
		if out, err := execIn(t, name, app, `psql "$DATABASE_URL" -tAc "select 'pooled'"`); err != nil || strings.TrimSpace(out) != "pooled" {
			t.Fatalf("%s: query through the pool: %v\n%s", app, err, out)
		}
		// TLS is required on the client side.
		if out, err := execIn(t, name, app, `psql "host=$PGHOST port=$PGPORT dbname=$PGDATABASE sslmode=disable" -tAc 'select 1'`); err == nil {
			t.Errorf("%s logged in to the pool without TLS:\n%s", app, out)
		}
		// The admin console is for docker exec only.
		if out, err := execIn(t, name, app, `psql "host=$PGHOST port=$PGPORT dbname=pgbouncer sslmode=verify-full" -tAc 'SHOW POOLS'`); err == nil {
			t.Errorf("%s reached the admin console:\n%s", app, out)
		}
	}

	// Both apps run more slow queries at once than the pool has server
	// connections; meanwhile Postgres is asked who is connected.
	ip := func(service string) string {
		return strings.TrimSpace(docker(t, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", containerOf(t, name, service)))
	}
	poolIP, pgIP := ip("pool"), ip("postgres")
	var wg sync.WaitGroup
	errs := make(chan string, 2)
	for _, app := range []string{"orders", "billing"} {
		wg.Go(func() {
			// 4 concurrent clients per app, each holding a transaction for 3s.
			out, err := execIn(t, name, app, `for i in 1 2 3 4; do psql "$DATABASE_URL" -tAc 'select pg_sleep(3)' & done; wait`)
			if err != nil {
				errs <- fmt.Sprintf("%s: %v\n%s", app, err, out)
			}
		})
	}
	var peak int
	var seen []string
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		// The Postgres health check also logs in as app, from Postgres's own
		// address; leave it out.
		for _, r := range psql(t, name, fmt.Sprintf("select host(a.client_addr) || '/' || s.ssl from pg_stat_activity a join pg_stat_ssl s using (pid) where a.usename = 'app' and host(a.client_addr) <> '%s'", pgIP)) {
			n++
			if r != poolIP+"/true" {
				t.Errorf("connection %s, want %s/true (PgBouncer, over TLS)", r, poolIP)
			}
			seen = append(seen, r)
		}
		peak = max(peak, n)
		time.Sleep(300 * time.Millisecond)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("load: %s", e)
	}
	if peak == 0 {
		t.Fatal("Postgres saw no connection from the pool")
	}
	if peak > poolSize {
		t.Errorf("Postgres saw %d server connections, more than pool_size %d", peak, poolSize)
	}
	t.Logf("Postgres saw at most %d server connections (pool_size %d), all from the pool over TLS: %d samples", peak, poolSize, len(seen))

	// The pools explorer's script works against the infra pool unchanged.
	out := docker(t, "exec", containerOf(t, name, "pool"), "sh", "-c", pgbouncerScript, "bb-pgbouncer", "6432")
	pools, err := parsePgBouncer(out)
	if err != nil || len(pools) != 1 {
		t.Fatalf("pools = %+v, %v\n%s", pools, err, out)
	}
	p := pools[0]
	if p.Database != "app" || p.User != "app" || p.ServersActive+p.ServersIdle+p.ServersUsed+p.ServersLogin > poolSize || p.Transactions == 0 {
		t.Errorf("pool = %+v", p)
	}
	t.Logf("SHOW POOLS: %+v", p)

	// Terminate: the second pool logs in to Postgres without TLS, from its
	// address on Postgres's private segment.
	if out, err := execIn(t, name, "orders", `psql "host=pool-plain.bb.internal port=6432 dbname=$PGDATABASE user=$PGUSER sslmode=verify-full" -tAc "select 'plain pooled'"`); err != nil || strings.TrimSpace(out) != "plain pooled" {
		t.Fatalf("query through the plaintext pool: %v\n%s", err, out)
	}
	var segIP string
	for _, f := range strings.Fields(docker(t, "inspect", "-f", `{{range $k, $v := .NetworkSettings.Networks}}{{$k}}={{$v.IPAddress}} {{end}}`, containerOf(t, name, "pool-plain"))) {
		if k, v, _ := strings.Cut(f, "="); strings.HasSuffix(k, "_plain-postgres") {
			segIP = v
		}
	}
	if !strings.HasPrefix(segIP, "10.213.") {
		t.Fatalf("pool-plain's segment address = %q", segIP)
	}
	rows := psql(t, name, "select host(a.client_addr) || '/' || s.ssl from pg_stat_activity a join pg_stat_ssl s using (pid) where a.usename = 'app' and host(a.client_addr) = '"+segIP+"'")
	if len(rows) == 0 || rows[0] != segIP+"/false" {
		t.Errorf("the plaintext pool's server connections = %v, want %s/false", rows, segIP)
	}

	// The record-shaped reason added no HBA rule and swallowed none:
	// Postgres loaded exactly the three generated rules (review B1).
	rules := psql(t, name, "select type || '|' || coalesce(address, 'none') || '|' || auth_method || '|' || coalesce(error, 'ok') from pg_hba_file_rules order by rule_number")
	if len(rules) != 3 || rules[0] != "local|none|trust|ok" || rules[1] != "hostssl|all|scram-sha-256|ok" ||
		!strings.HasPrefix(rules[2], "hostnossl|10.213.") || !strings.HasSuffix(rules[2], "|scram-sha-256|ok") {
		t.Errorf("Postgres's HBA rules = %q, want local trust, hostssl all, hostnossl from the segment", rules)
	}
	// A client only on the default network cannot reach Postgres in
	// plaintext, even with the right password; over TLS it can, so the
	// refusal is the HBA's, not the network's.
	if out, err := execIn(t, name, "orders", `psql "host=postgres.bb.internal port=5432 dbname=$PGDATABASE user=$PGUSER sslmode=verify-full sslrootcert=$PGSSLROOTCERT" -tAc "select 'tls'"`); err != nil || strings.TrimSpace(out) != "tls" {
		t.Fatalf("orders cannot reach Postgres over TLS: %v\n%s", err, out)
	}
	out, err = execIn(t, name, "orders", `psql "host=postgres.bb.internal port=5432 dbname=$PGDATABASE user=$PGUSER sslmode=disable" -tAc 'select 1'`)
	if err == nil || !strings.Contains(out, "no pg_hba.conf entry") {
		t.Errorf("a default-network client in plaintext: %v\n%s", err, out)
	}
}
