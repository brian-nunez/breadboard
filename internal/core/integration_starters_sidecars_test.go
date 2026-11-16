//go:build integration

package core

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sidecarStarterLab applies one starter to a fresh experiment, runs up, and
// checks on cleanup that nothing labelled for the experiment survives.
func sidecarStarterLab(t *testing.T, prefix, id string) (*Engine, string, string) {
	t.Helper()
	var name string
	// Registered first, so it runs after starterLab's down.
	t.Cleanup(func() {
		if name != "" {
			assertGone(t, name)
		}
	})
	eng, dir, n := starterLab(t, prefix, id)
	name = n
	return eng, dir, name
}

// lastLines logs the tail of each app's log when the test failed.
func lastLines(t *testing.T, eng *Engine, dir string, services ...string) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, s := range services {
			lines := logsOf(t, eng, dir, s)
			t.Logf("%s (last 20 lines):\n%s", s, strings.Join(lines[max(0, len(lines)-20):], "\n"))
		}
	})
}

// TestIntegrationStarterSidecarAmbassador: the caller's plain loopback calls
// are balanced by its Envoy sidecar across more than one backend replica.
func TestIntegrationStarterSidecarAmbassador(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "sc-amb", "sidecar/envoy-ambassador")
	lastLines(t, eng, dir, "ambassador-caller")
	answered := regexp.MustCompile(`answered by (ambassador-backend-\d+)`)
	var seen []string
	eventually(t, 2*time.Minute, "answers from every replica", func() bool {
		seen = matches(logsOf(t, eng, dir, "ambassador-caller"), answered)
		slices.Sort(seen)
		seen = slices.Compact(seen)
		return len(seen) == 3
	})
	t.Logf("replicas that answered: %v", seen)

	cl, err := eng.EnvoyClusters(context.Background(), ExploreRequest{Dir: dir, Service: "ambassador-caller"})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cl.Clusters, func(c EnvoyCluster) bool { return c.Name == "outbound_ambassador-backend" })
	if i < 0 {
		t.Fatalf("no outbound cluster: %+v", cl.Clusters)
	}
	busy := 0
	for _, h := range cl.Clusters[i].Hosts {
		if h.Requests > 0 {
			busy++
		}
		t.Logf("host %s (%s): %d requests", h.Address, h.Instance, h.Requests)
	}
	if len(cl.Clusters[i].Hosts) != 3 || busy < 2 {
		t.Fatalf("explorer: %d hosts, %d with requests", len(cl.Clusters[i].Hosts), busy)
	}
}

// TestIntegrationStarterSidecarTLSTermination: the plaintext app is reached
// over verified TLS through its sidecar, never directly.
func TestIntegrationStarterSidecarTLSTermination(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "sc-tls", "sidecar/envoy-tls-termination")
	lastLines(t, eng, dir, "tls-client", "plain-web")
	tlsOK := regexp.MustCompile(`https :8443 (200 OK over TLS 1\.[23], certificate for \[plain-web\.bb\.internal\] verified: hello from plain-web)`)
	refused := regexp.MustCompile(`(http :8080 refused, as it should be: .*connection refused)`)
	eventually(t, 2*time.Minute, "a verified TLS call and a refused plaintext call", func() bool {
		lines := logsOf(t, eng, dir, "tls-client")
		return len(matches(lines, tlsOK)) > 0 && len(matches(lines, refused)) > 0
	})
	if lines := logsOf(t, eng, dir, "tls-client"); slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "answered") }) {
		t.Fatal("the app's plaintext port answered another container")
	}
	seen := matches(logsOf(t, eng, dir, "plain-web"), regexp.MustCompile(`request from (127\.0\.0\.1:\d+, tls=false, client used https)`))
	if len(seen) == 0 {
		t.Fatal("plain-web did not log a plaintext request from its sidecar on loopback")
	}
	t.Logf("tls-client: %s; plain-web saw: %s", matches(logsOf(t, eng, dir, "tls-client"), tlsOK)[0], seen[0])
}

// TestIntegrationStarterSidecarTransparent: a plain HTTP call by name to a
// declared service arrives over TLS; the undeclared service is refused by
// the caller's own sidecar.
func TestIntegrationStarterSidecarTransparent(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "sc-tp", "sidecar/envoy-transparent")
	lastLines(t, eng, dir, "transparent-caller")
	declared := regexp.MustCompile(`declared http://transparent-backend\.bb\.internal:8443/: (200 OK: transparent-backend, reached over https)`)
	// The undeclared address is refused by closing the connection: no HTTP answer.
	refused := regexp.MustCompile(`undeclared http://transparent-stranger\.bb\.internal:8443/: (refused: .*EOF.*)`)
	// A declared address with an undeclared Host reaches the HTTP route and gets 403.
	wrongHost := regexp.MustCompile(`wrong-host http://transparent-backend\.bb\.internal:8443/: (403 Forbidden: breadboard: egress denied.*)`)
	eventually(t, 2*time.Minute, "a declared call over TLS, a closed undeclared call and a 403 for an undeclared Host", func() bool {
		lines := logsOf(t, eng, dir, "transparent-caller")
		return len(matches(lines, declared)) > 0 && len(matches(lines, refused)) > 0 && len(matches(lines, wrongHost)) > 0
	})
	if slices.ContainsFunc(logsOf(t, eng, dir, "transparent-stranger"), func(l string) bool { return strings.Contains(l, "reached") }) {
		t.Fatal("the stranger logged a request")
	}
	if n := envoyStat(t, name, "transparent-caller-envoy", "egress_denied.rbac.denied"); n == 0 {
		t.Fatal("the sidecar counted no closed connections")
	}
	if n := envoyStat(t, name, "transparent-caller-envoy", "http.transparent_http_8443.downstream_rq_4xx"); n == 0 {
		t.Fatal("the sidecar counted no 403 answers")
	}
	lines := logsOf(t, eng, dir, "transparent-caller")
	t.Logf("declared: %s; undeclared: %s; wrong host: %s", matches(lines, declared)[0], matches(lines, refused)[0], matches(lines, wrongHost)[0])
}

// TestIntegrationStarterSidecarCircuitBreaking: a failing replica is not
// ejected while max_ejection_percent forbids it, is ejected once the limit
// is raised in a user-owned copy of the generated Envoy config (envoy.file),
// and gets traffic again after the ejection time once it recovers.
func TestIntegrationStarterSidecarCircuitBreaking(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "sc-cb", "sidecar/circuit-breaking")
	lastLines(t, eng, dir, "breaker-caller", "breaker-backend-3")
	window := regexp.MustCompile(`last 5s: (answered .*)`)
	windows := func(skip int) []string {
		lines := logsOf(t, eng, dir, "breaker-caller")
		return matches(lines[min(skip, len(lines)):], window)
	}
	eventually(t, 2*time.Minute, "every replica to answer in one window", func() bool {
		return slices.ContainsFunc(windows(0), func(w string) bool {
			return strings.Contains(w, "breaker-backend-1=") && strings.Contains(w, "breaker-backend-2=") && strings.Contains(w, "breaker-backend-3=") && strings.HasSuffix(w, "failed none")
		})
	})
	cluster := func() EnvoyCluster {
		cl, err := eng.EnvoyClusters(context.Background(), ExploreRequest{Dir: dir, Service: "breaker-caller"})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cl.Clusters {
			if c.Name == "outbound_breaker-backend" {
				return c
			}
		}
		t.Fatalf("no outbound cluster: %+v", cl.Clusters)
		return EnvoyCluster{}
	}

	fail := func() {
		t.Helper()
		if out, err := execIn(t, name, "breaker-backend-3", "wget -qO- --post-data= http://127.0.0.1:8080/fail"); err != nil {
			t.Fatalf("fail replica 3: %v\n%s", err, out)
		}
	}

	// The starter's max_ejection_percent: 50 lets Envoy eject one host of three.
	fail()
	eventually(t, time.Minute, "replica 3 to be ejected", func() bool {
		return slices.ContainsFunc(cluster().Hosts, func(h EnvoyHost) bool { return h.Ejected && strings.HasPrefix(h.Instance, "breaker-backend-3") })
	})
	if c := cluster(); c.EjectionsActive != 1 || c.EjectionsOverflow != 0 {
		t.Errorf("ejections active %d, overflow %d; want 1 and 0", c.EjectionsActive, c.EjectionsOverflow)
	}
	skip := len(logsOf(t, eng, dir, "breaker-caller"))
	eventually(t, 20*time.Second, "a window answered by replicas 1 and 2 only", func() bool {
		return slices.ContainsFunc(windows(skip), func(w string) bool {
			return !strings.Contains(w, "breaker-backend-3") && strings.HasSuffix(w, "failed none")
		})
	})
	t.Logf("while ejected: %s", windows(skip)[len(windows(skip))-1])

	if out, err := execIn(t, name, "breaker-backend-3", "wget -qO- --post-data= http://127.0.0.1:8080/recover"); err != nil {
		t.Fatalf("recover replica 3: %v\n%s", err, out)
	}
	recoveredAt := time.Now()
	skip = len(logsOf(t, eng, dir, "breaker-caller"))
	eventually(t, 2*time.Minute, "replica 3 to answer again after its ejection time", func() bool {
		return slices.ContainsFunc(windows(skip), func(w string) bool {
			return strings.Contains(w, "breaker-backend-3=") && strings.HasSuffix(w, "failed none")
		})
	})
	t.Logf("replica 3 answered again %s after recovering", time.Since(recoveredAt).Round(time.Second))

	// The README's exercise: Envoy's default limit (10%) ejects no host of
	// three. Changing the knob in experiment.yaml recreates the sidecar.
	specPath := filepath.Join(dir, "experiment.yaml")
	src := readFile(t, specPath)
	knob := "max_ejection_percent: 50"
	if !strings.Contains(src, knob) {
		t.Fatalf("experiment.yaml has no %q:\n%s", knob, src)
	}
	if err := os.WriteFile(specPath, []byte(strings.Replace(src, knob, "max_ejection_percent: 10", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	upHealthy(t, eng, dir)
	gen := readFile(t, filepath.Join(dir, ".bb", "runtime", "config", "breaker-caller-envoy", "envoy.yaml"))
	if !strings.Contains(gen, "max_ejection_percent: 10") {
		t.Fatalf("generated config did not follow the knob:\n%s", gen)
	}
	fail()
	eventually(t, time.Minute, "ejections blocked by max_ejection_percent", func() bool {
		c := cluster()
		return c.EjectionsOverflow > 0 && c.EjectionsActive == 0
	})
	t.Logf("with max_ejection_percent: 10, %d ejections overflowed", cluster().EjectionsOverflow)
}

// TestIntegrationStarterSidecarPgBouncerPool: 50 client connections share
// at most pool_size (10) Postgres connections.
func TestIntegrationStarterSidecarPgBouncerPool(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "sc-pgb", "sidecar/pgbouncer-pool")
	lastLines(t, eng, dir, "pool-client")
	stat := regexp.MustCompile(`clients=50 queries=(\d+) in 5s; postgres sees (\d+) server connections`)
	eventually(t, 2*time.Minute, "a report with 50 clients and queries", func() bool {
		for _, l := range logsOf(t, eng, dir, "pool-client") {
			if m := stat.FindStringSubmatch(l); m != nil && m[1] != "0" {
				return true
			}
		}
		return false
	})
	for _, l := range logsOf(t, eng, dir, "pool-client") {
		if m := stat.FindStringSubmatch(l); m != nil {
			if n, _ := strconv.Atoi(m[2]); n > 10 {
				t.Errorf("the app saw %d server connections: %s", n, l)
			}
		}
	}
	rows := psql(t, name, "select count(*) from pg_stat_activity where usename = 'app'")
	if n, _ := strconv.Atoi(rows[0]); n == 0 || n > 10 {
		t.Fatalf("Postgres has %d connections for the app's role, want 1..10", n)
	}
	pools, err := eng.PgBouncerPools(context.Background(), ExploreRequest{Dir: dir, Service: "pool-client"})
	if err != nil {
		t.Fatal(err)
	}
	var clients, servers int64
	for _, p := range pools.Pools {
		clients += p.ClientsActive + p.ClientsWaiting
		servers += p.ServersActive + p.ServersIdle + p.ServersUsed + p.ServersLogin
	}
	if clients < 50 || servers > 10 {
		t.Fatalf("PgBouncer: %d clients, %d servers (want ≥ 50 and ≤ 10)", clients, servers)
	}
	t.Logf("Postgres sees %s connections; PgBouncer: %d clients on %d servers, mode %s", rows[0], clients, servers, pools.PoolMode)
}

// TestIntegrationStarterSidecarLogShipper: lines the writer puts in a file
// reach the bucket through Fluent Bit, where the reader finds them with the
// AWS SDK.
func TestIntegrationStarterSidecarLogShipper(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "sc-log", "sidecar/log-shipper")
	lastLines(t, eng, dir, "log-reader", "log-writer-fluent-bit")
	found := regexp.MustCompile(`\d+ objects in app-logs; newest (log-writer/\d{4}/\d{2}/\d{2}/\S+\.jsonl starts with .*order placed.*)`)
	var hits []string
	eventually(t, 3*time.Minute, "the reader to find a log object", func() bool {
		hits = matches(logsOf(t, eng, dir, "log-reader"), found)
		return len(hits) > 0
	})
	t.Logf("log-reader: %s", hits[0])
}
