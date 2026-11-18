//go:build integration

package core

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIntegrationStarterL4VsL7: on one connection, the mode http listener
// routes every request to the right app and spreads them over replicas; the
// mode tcp listener keeps each connection on one server and cannot route by
// path.
func TestIntegrationStarterL4VsL7(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "px-l4l7", "proxy/l4-vs-l7")
	lastLines(t, eng, dir, "l4l7-client", "l4l7-edge")

	l7 := regexp.MustCompile(`L7 :8443 \(mode http\): (1 connection\(s\), 8 requests; instances \[([^\]]*)\]; misrouted 0 of 8)`)
	l4 := regexp.MustCompile(`L4 :7443 \(mode tcp\): (4 connections, 8 requests; instances \[[^\]]*\]; one instance per connection: true; misrouted [1-9]\d* of 8)`)

	var l7Line, l4Line string

	eventually(t, 3*time.Minute, "a layer 7 round on one connection reaching both api replicas, and a layer 4 round with misrouted requests", func() bool {
		lines := logsOf(t, eng, dir, "l4l7-client")
		for _, l := range lines {
			if m := l7.FindStringSubmatch(l); m != nil && strings.Contains(m[2], "l4l7-api-1") && strings.Contains(m[2], "l4l7-api-2") {
				l7Line = m[1]
			}
		}

		if got := matches(lines, l4); len(got) > 0 {
			l4Line = got[len(got)-1]
		}

		return l7Line != "" && l4Line != ""
	})

	lines := logsOf(t, eng, dir, "l4l7-client")
	if slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "one instance per connection: false") }) {
		t.Error("a layer 4 connection reached two instances")
	}

	if slices.ContainsFunc(lines, func(l string) bool {
		return strings.Contains(l, "(mode http)") && !strings.Contains(l, "misrouted 0 of")
	}) {
		t.Error("layer 7 misrouted a request")
	}

	st, err := eng.HAProxyStats(context.Background(), ExploreRequest{Dir: dir, Service: "l4l7-edge"})
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range st.Frontends {
		t.Logf("frontend %s (%s): %d sessions, %d requests", f.Name, f.Mode, f.TotalSessions, f.Requests)

		switch f.Name {
		case "l7":
			if f.Mode != "http" || f.Requests == 0 {
				t.Errorf("frontend l7: mode %s, %d requests", f.Mode, f.Requests)
			}
		case "l4":
			if f.Mode != "tcp" || f.TotalSessions == 0 || f.Requests != 0 {
				t.Errorf("frontend l4: mode %s, %d sessions, %d requests", f.Mode, f.TotalSessions, f.Requests)
			}
		}
	}

	t.Logf("layer 7: %s", l7Line)
	t.Logf("layer 4: %s", l4Line)
}

// TestIntegrationStarterTLSModes: the client is shown the proxy's
// certificate for re-encrypt and terminate and the backend's for
// passthrough; the terminate backend's plaintext port answers the proxy
// and nobody else.
func TestIntegrationStarterTLSModes(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "px-tlsm", "proxy/tls-modes")
	lastLines(t, eng, dir, "tlsm-client", "tlsm-edge")

	cert := func(mode, names, backend string) *regexp.Regexp {
		return regexp.MustCompile(mode + `\s+\S+: (200 OK, certificate for \[` + regexp.QuoteMeta(names) + `\].*backend: ` + backend + `)`)
	}
	reencrypt := cert("re-encrypt", "tlsm-edge.bb.internal", `tlsm-reencrypt: TLS 1\.[23] from .*SNI tlsm-reencrypt\.bb\.internal, X-Forwarded-Proto https`)
	terminate := cert("terminate", "tlsm-edge.bb.internal", `tlsm-terminate: plaintext HTTP from .*\(tls=false\), X-Forwarded-Proto https`)
	passthrough := cert("passthrough", "tlsm-passthrough.bb.internal", `tlsm-passthrough: TLS 1\.[23] session from the client itself .*SNI tlsm-passthrough\.bb\.internal, X-Forwarded-Proto none`)
	direct := regexp.MustCompile(`(direct http://tlsm-terminate\.bb\.internal:8080/: unreachable, as it should be: .*)`)
	directPlain := regexp.MustCompile(`(direct http://tlsm-terminate\.plain\.bb\.internal:8080/: unreachable, as it should be: .*)`)
	all := []*regexp.Regexp{reencrypt, terminate, passthrough, direct, directPlain}

	var lines []string

	eventually(t, 3*time.Minute, "a call in every mode and refused direct calls", func() bool {
		lines = logsOf(t, eng, dir, "tlsm-client")

		return !slices.ContainsFunc(all, func(re *regexp.Regexp) bool { return len(matches(lines, re)) == 0 })
	})

	if slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "the terminate hop is exposed") }) {
		t.Fatal("the terminate backend's plaintext port answered the client")
	}

	for _, re := range all {
		t.Log(matches(lines, re)[0])
	}

	// The plaintext port by address: the proxy, on the segment, reaches it;
	// another container does not.
	addr := strings.TrimSpace(docker(t, "exec", containerOf(t, name, "tlsm-terminate"), "printenv", "BB_PLAIN_LISTEN_ADDR"))

	ip, port, _ := strings.Cut(addr, ":")
	if out, err := dockerMay("exec", containerOf(t, name, "tlsm-edge"), "bash", "-c", "exec 3<>/dev/tcp/"+ip+"/"+port+" && printf 'GET / HTTP/1.0\\r\\n\\r\\n' >&3 && head -1 <&3"); err != nil || !strings.Contains(out, "200 OK") {
		t.Fatalf("the proxy cannot reach %s: %v\n%s", addr, err, out)
	}

	if out, err := execIn(t, name, "tlsm-client", "wget -qO- -T 2 http://"+addr+"/"); err == nil {
		t.Fatalf("tlsm-client reached the plaintext hop at %s:\n%s", addr, out)
	}

	if out, err := execIn(t, name, "tlsm-reencrypt", "wget -qO- -T 2 http://"+addr+"/"); err == nil {
		t.Fatalf("tlsm-reencrypt reached the plaintext hop at %s:\n%s", addr, out)
	}

	t.Logf("%s answers the proxy only", addr)
}

// TestIntegrationStarterRequestMirroring: v2 receives copies of the
// client's requests, and the client only ever sees v1's answers.
func TestIntegrationStarterRequestMirroring(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "px-mirror", "proxy/request-mirroring")
	lastLines(t, eng, dir, "mirror-client", "mirror-v2", "mirror-edge")
	envoyConfigValid(t, name, "mirror-edge")

	window := regexp.MustCompile(`last 5s: (\d+) orders, answered by v1=(\d+) v2=(\d+), failed (\d+)`)
	copies := regexp.MustCompile(`(last 5s: received [1-9]\d* mirrored requests \(Host mirror-edge\.bb\.internal-shadow:8443\), answered 500 to [1-9]\d*.*)`)
	clean := 0

	eventually(t, 3*time.Minute, "three clean client windows and v2 receiving copies", func() bool {
		clean = 0

		for _, l := range logsOf(t, eng, dir, "mirror-client") {
			if m := window.FindStringSubmatch(l); m != nil && m[2] != "0" && m[3] == "0" && m[4] == "0" {
				clean++
			}
		}

		return clean >= 3 && len(matches(logsOf(t, eng, dir, "mirror-v2"), copies)) > 0
	})

	var v1 int

	for _, l := range logsOf(t, eng, dir, "mirror-client") {
		if m := window.FindStringSubmatch(l); m != nil {
			if m[3] != "0" {
				t.Errorf("the client got an answer from v2: %s", l)
			}

			n, _ := strconv.Atoi(m[2])
			v1 += n
		}

		if strings.Contains(l, ": 500 ") || strings.Contains(l, "v2 bug") {
			t.Errorf("a v2 failure reached the client: %s", l)
		}
	}

	cl, err := eng.EnvoyClusters(context.Background(), ExploreRequest{Dir: dir, Service: "mirror-edge"})
	if err != nil {
		t.Fatal(err)
	}

	reqs, errs := map[string]int64{}, map[string]int64{}

	for _, c := range cl.Clusters {
		for _, h := range c.Hosts {
			reqs[c.Name] += h.Requests
			errs[c.Name] += h.Errors
			t.Logf("cluster %s host %s (%s): %d requests, %d errors", c.Name, h.Address, h.Instance, h.Requests, h.Errors)
		}
	}

	if reqs["v1"] == 0 || reqs["v2"] == 0 {
		t.Fatalf("Envoy counted v1=%d v2=%d requests", reqs["v1"], reqs["v2"])
	}
	// v2's failures stay inside Envoy: counted on the shadow cluster, never
	// on v1, never at the client.
	if errs["v1"] != 0 || errs["v2"] == 0 {
		t.Errorf("Envoy counted errors v1=%d v2=%d; want 0 and some", errs["v1"], errs["v2"])
	}

	t.Logf("the client got %d answers, all from v1; v2: %s", v1, matches(logsOf(t, eng, dir, "mirror-v2"), copies)[0])
}

// TestIntegrationStarterConnectionDraining: under constant load, one
// replica is drained at the proxy and stopped, and another is stopped and
// drains itself, with no failed request.
func TestIntegrationStarterConnectionDraining(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "px-drain", "proxy/connection-draining")
	lastLines(t, eng, dir, "drain-client", "drain-edge")

	window := regexp.MustCompile(`last 2s: (.*), failed (\d+); total (\d+) requests, (\d+) failed`)
	windows := func(skip int) [][]string {
		var out [][]string

		lines := logsOf(t, eng, dir, "drain-client")
		for _, l := range lines[min(skip, len(lines)):] {
			if m := window.FindStringSubmatch(l); m != nil {
				out = append(out, m)
			}
		}

		return out
	}

	eventually(t, 2*time.Minute, "every replica to answer in one window with no failures", func() bool {
		return slices.ContainsFunc(windows(0), func(m []string) bool {
			return strings.Contains(m[1], "drain-backend-1=") && strings.Contains(m[1], "drain-backend-2=") && strings.Contains(m[1], "drain-backend-3=") && m[4] == "0"
		})
	})

	server := func(s string) HAProxyServer {
		st, err := eng.HAProxyStats(context.Background(), ExploreRequest{Dir: dir, Service: "drain-edge"})
		if err != nil {
			t.Fatal(err)
		}

		for _, b := range st.Backends {
			for _, srv := range b.Servers {
				if b.Name == "be_app" && srv.Name == s {
					return srv
				}
			}
		}

		t.Fatalf("no server be_app/%s in %+v", s, st.Backends)

		return HAProxyServer{}
	}

	// 1. Drain replica 2 through the runtime API, then stop it.
	out := docker(t, "exec", containerOf(t, name, "drain-edge"), "bash", "-c",
		`exec 3<>/dev/tcp/127.0.0.1/9999; echo "set server be_app/drain-backend-2 state drain" >&3; cat <&3`)
	t.Logf("runtime API: %q", strings.TrimSpace(out))
	eventually(t, 30*time.Second, "replica 2 to be in DRAIN with no sessions", func() bool {
		s := server("drain-backend-2")

		return strings.Contains(s.Status, "DRAIN") && s.CurrentSessions == 0
	})
	skip := len(logsOf(t, eng, dir, "drain-client"))
	eventually(t, 20*time.Second, "a window without replica 2", func() bool {
		return slices.ContainsFunc(windows(skip), func(m []string) bool { return !strings.Contains(m[1], "drain-backend-2") })
	})
	docker(t, "stop", "-t", "15", containerOf(t, name, "drain-backend-2"))
	t.Log("replica 2 drained and stopped")

	// 2. Stop replica 3 without telling the proxy: it fails its health check
	// on SIGTERM, keeps serving, and exits once HAProxy has stopped sending.
	id3 := containerOf(t, name, "drain-backend-3")
	stopped := time.Now()

	docker(t, "stop", "-t", "15", id3)
	t.Logf("replica 3 stopped in %s", time.Since(stopped).Round(100*time.Millisecond))

	logs3 := docker(t, "logs", id3)
	if code := strings.TrimSpace(docker(t, "inspect", "-f", "{{.State.ExitCode}}", id3)); code != "0" {
		t.Errorf("replica 3 exited with %s, want 0 (a clean exit after draining)", code)
	}

	for _, want := range []string{"SIGTERM; failing /healthz", "closing; waiting for", "drained, exiting"} {
		if !strings.Contains(logs3, want) {
			t.Errorf("replica 3 did not log %q:\n%s", want, logs3)
		}
	}

	skip = len(logsOf(t, eng, dir, "drain-client"))
	eventually(t, 20*time.Second, "two windows answered by replica 1 alone", func() bool {
		n := 0

		for _, m := range windows(skip) {
			if strings.HasPrefix(m[1], "drain-backend-1=") && !strings.Contains(m[1], " ") {
				n++
			}
		}

		return n >= 2
	})

	ws := windows(0)

	last := ws[len(ws)-1]
	if last[4] != "0" || slices.ContainsFunc(logsOf(t, eng, dir, "drain-client"), func(l string) bool { return strings.Contains(l, "request failed") }) {
		t.Fatalf("requests failed: %s", last[0])
	}

	t.Logf("after draining two replicas: %s", last[0])
}

// TestIntegrationStarterPoolingModes: 30 clients share at most pool_size
// (5) server connections in the transaction pool, every session client
// keeps its own, and the SET trap shows session state going wrong in
// transaction mode only.
func TestIntegrationStarterPoolingModes(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "px-pool", "pgbouncer/pooling-modes")
	lastLines(t, eng, dir, "pool-modes")

	txn := regexp.MustCompile(`transaction pool \(app\): 30 clients ran (\d+) queries in 5s on (\d+) server connections`)
	session := regexp.MustCompile(`session pool \(app_session\): 6 clients ran (\d+) queries in 5s on (\d+) server connections; a client's next query changed server (\d+) times`)
	setWrong := regexp.MustCompile(`(trap SET \(transaction\): .*WRONG.*)`)
	prepareWrong := regexp.MustCompile(`(trap PREPARE \(transaction\): .*WRONG: .*does not exist.*)`)
	setOK := regexp.MustCompile(`(trap SET \(session\): .*: ok)$`)

	eventually(t, 4*time.Minute, "busy pools and both traps sprung in transaction mode", func() bool {
		lines := logsOf(t, eng, dir, "pool-modes")
		busy := slices.ContainsFunc(lines, func(l string) bool {
			m := txn.FindStringSubmatch(l)

			return m != nil && m[1] != "0"
		})

		return busy && len(matches(lines, setWrong)) > 0 && len(matches(lines, prepareWrong)) > 0 && len(matches(lines, setOK)) > 0
	})

	lines := logsOf(t, eng, dir, "pool-modes")
	for _, l := range lines {
		if m := txn.FindStringSubmatch(l); m != nil {
			if n, _ := strconv.Atoi(m[2]); n > 5 {
				t.Errorf("the transaction pool used %d server connections: %s", n, l)
			}
		}

		if m := session.FindStringSubmatch(l); m != nil {
			if n, _ := strconv.Atoi(m[2]); n > 6 || m[3] != "0" {
				t.Errorf("a session client changed server, or the pool grew: %s", l)
			}
		}

		if strings.Contains(l, "(session)") && strings.Contains(l, "WRONG") {
			t.Errorf("session mode fell into a trap: %s", l)
		}
	}

	rows := psql(t, name, "select count(*) from pg_stat_activity where usename = 'app'")
	if n, _ := strconv.Atoi(rows[0]); n == 0 || n > 13 {
		t.Fatalf("Postgres has %d connections for the app's role, want 1..13 (5 + 8)", n)
	}

	pools, err := eng.PgBouncerPools(context.Background(), ExploreRequest{Dir: dir, Service: "pool-central"})
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}

	for _, p := range pools.Pools {
		clients := p.ClientsActive + p.ClientsWaiting
		servers := p.ServersActive + p.ServersIdle + p.ServersUsed + p.ServersLogin
		t.Logf("pool %s (%s): %d clients on %d servers", p.Database, p.PoolMode, clients, servers)

		switch p.Database {
		case "app":
			seen["app"] = true

			if p.PoolMode != "transaction" || clients < 30 || servers > 5 {
				t.Errorf("pool app: mode %s, %d clients, %d servers (want transaction, ≥ 30, ≤ 5)", p.PoolMode, clients, servers)
			}
		case "app_session":
			seen["app_session"] = true

			if p.PoolMode != "session" || servers > 8 {
				t.Errorf("pool app_session: mode %s, %d servers (want session, ≤ 8)", p.PoolMode, servers)
			}
		}
	}

	if !seen["app"] || !seen["app_session"] {
		t.Fatalf("PgBouncer pools: %+v", pools.Pools)
	}

	t.Logf("Postgres sees %s connections; %s; %s; %s", rows[0], matches(lines, setWrong)[0], matches(lines, prepareWrong)[0], matches(lines, setOK)[0])
}
