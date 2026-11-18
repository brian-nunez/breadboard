//go:build integration

package core

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lbWindow is one 5-second tally a load-balancing starter's client logs:
// "<label>last 5s: N requests | a-1=x (p%) ... | status 200=n ... | p50 … p99 …".
type lbWindow struct {
	line     string
	total    int
	replicas map[string]int
	statuses map[string]int
}

var lbWindowRe = regexp.MustCompile(`last 5s: (\d+) requests \| ([^|]*)\| status ([^|]*)\|`)

// lbWindows parses the windows of lines that contain label, from line skip on.
func lbWindows(lines []string, label string, skip int) []lbWindow {
	var out []lbWindow

	for _, l := range lines[min(skip, len(lines)):] {
		if !strings.Contains(l, label) {
			continue
		}

		m := lbWindowRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}

		w := lbWindow{line: l, replicas: map[string]int{}, statuses: map[string]int{}}

		w.total, _ = strconv.Atoi(m[1])
		for _, f := range strings.Fields(m[2]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				w.replicas[k], _ = strconv.Atoi(v)
			}
		}

		for _, f := range strings.Fields(m[3]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				w.statuses[k], _ = strconv.Atoi(v)
			}
		}

		out = append(out, w)
	}

	return out
}

// share is the replica's part of the window's requests.
func (w lbWindow) share(replica string) float64 {
	if w.total == 0 {
		return 0
	}

	return float64(w.replicas[replica]) / float64(w.total)
}

// allOK reports whether every request in the window got a 200.
func (w lbWindow) allOK() bool { return w.total > 0 && w.statuses["200"] == w.total }

// balanced reports whether each of the n replicas named prefix-1..n served
// between lo and hi of the window, and no other replica answered.
func (w lbWindow) balanced(prefix string, n int, lo, hi float64) bool {
	if !w.allOK() || len(w.replicas) != n {
		return false
	}

	for i := 1; i <= n; i++ {
		s := w.share(prefix + "-" + strconv.Itoa(i))
		if s < lo || s > hi {
			return false
		}
	}

	return true
}

// TestIntegrationStarterLBRoundRobin: NGINX, running the starter's own
// config, gives each of three replicas about a third of the requests, and
// a fourth replica added with bb up gets a quarter without NGINX restarting.
func TestIntegrationStarterLBRoundRobin(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-rr", "lb/round-robin")
	lastLines(t, eng, dir, "rr-client", "rr-edge")

	var first int

	eventually(t, 2*time.Minute, "a window answered by all three replicas", func() bool {
		ws := lbWindows(logsOf(t, eng, dir, "rr-client"), "", 0)
		for i, w := range ws {
			if w.allOK() && len(w.replicas) == 3 && w.total >= 30 {
				first = i

				return true
			}
		}

		return false
	})
	// The three windows after the first full one: each replica gets a third.
	var ws []lbWindow

	eventually(t, time.Minute, "three more windows", func() bool {
		ws = lbWindows(logsOf(t, eng, dir, "rr-client"), "", 0)[first:]

		return len(ws) >= 4
	})

	for _, w := range ws[1:4] {
		if !w.balanced("rr-backend", 3, 0.25, 0.42) {
			t.Errorf("not a third each: %s", w.line)
		}
	}

	t.Logf("round robin: %s", ws[3].line)

	// Scaling: NGINX re-resolves rr-backend.<domain> and adds the fourth
	// replica as a peer, with no restart.
	edge := containerOf(t, name, "rr-edge")
	specPath := filepath.Join(dir, "experiment.yaml")

	src := readFile(t, specPath)
	if !strings.Contains(src, "replicas: 3") {
		t.Fatalf("experiment.yaml has no replicas: 3:\n%s", src)
	}

	if err := os.WriteFile(specPath, []byte(strings.Replace(src, "replicas: 3", "replicas: 4", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	upHealthy(t, eng, dir)

	if got := containerOf(t, name, "rr-edge"); got != edge {
		t.Errorf("bb up recreated rr-edge after scaling the backend")
	}
	// Windows before the scale-up never list four replicas, so all are read
	// (the client may have been recreated, which restarts its log).
	var w4 lbWindow

	eventually(t, time.Minute, "a window answered by four replicas, a quarter each", func() bool {
		for _, w := range lbWindows(logsOf(t, eng, dir, "rr-client"), "", 0) {
			if w.balanced("rr-backend", 4, 0.18, 0.32) {
				w4 = w

				return true
			}
		}

		return false
	})
	t.Logf("after scaling to 4: %s", w4.line)
}

// lbCompare waits until the client has logged, for each label, n windows
// in a row in which every request succeeded and all replicas answered, and
// returns those windows per label. Windows logged before every replica was
// up are skipped.
func lbCompare(t *testing.T, eng *Engine, dir, client string, replicas, n int, labels ...string) map[string][]lbWindow {
	t.Helper()

	out := map[string][]lbWindow{}

	eventually(t, 3*time.Minute, "full windows from every strategy", func() bool {
		lines := logsOf(t, eng, dir, client)
		for _, label := range labels {
			ws := lbWindows(lines, label, 0)
			run := 0

			for i, w := range ws {
				if w.allOK() && len(w.replicas) == replicas {
					run++
				} else {
					run = 0
				}

				if run == n {
					out[label] = ws[i-n+1 : i+1]

					break
				}
			}

			if run < n {
				return false
			}
		}

		return true
	})

	return out
}

// TestIntegrationStarterLBLeastConnections: with one replica ten times
// slower, HAProxy's leastconn sends it far fewer requests than the others,
// while roundrobin over the same replicas still sends it a third.
func TestIntegrationStarterLBLeastConnections(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "lb-lc", "lb/least-connections")
	lastLines(t, eng, dir, "lc-client", "lc-edge")

	ws := lbCompare(t, eng, dir, "lc-client", 3, 3, "leastconn ", "roundrobin")
	for _, w := range ws["leastconn "] {
		if s := w.share("lc-backend-3"); s >= 0.15 {
			t.Errorf("leastconn gave the slow replica %.0f%%: %s", s*100, w.line)
		}
	}

	for _, w := range ws["roundrobin"] {
		if !w.balanced("lc-backend", 3, 0.25, 0.42) {
			t.Errorf("roundrobin did not give each replica a third: %s", w.line)
		}
	}

	t.Logf("%s", ws["leastconn "][2].line)
	t.Logf("%s", ws["roundrobin"][2].line)

	// The explorer shows the same through HAProxy's own counters, and tags
	// be_leastconn with the starter's upstream.
	stats, err := eng.HAProxyStats(context.Background(), ExploreRequest{Dir: dir, Service: "lc-edge"})
	if err != nil {
		t.Fatal(err)
	}

	for _, b := range stats.Backends {
		if b.Name != "be_leastconn" {
			continue
		}

		if b.Upstream.Route != "leastconn" || b.Upstream.Target != "lc-backend" || b.Balance != "leastconn" {
			t.Errorf("be_leastconn: upstream %+v, balance %q", b.Upstream, b.Balance)
		}

		picks := map[string]int64{}
		for _, s := range b.Servers {
			picks[s.Instance] = s.Selected
		}

		if picks["lc-backend-3"]*3 >= picks["lc-backend-1"] || picks["lc-backend-3"]*3 >= picks["lc-backend-2"] {
			t.Errorf("HAProxy's picks under leastconn: %v", picks)
		}

		t.Logf("HAProxy picks under leastconn: %v", picks)

		return
	}

	t.Fatalf("no be_leastconn in %+v", stats.Backends)
}

// TestIntegrationStarterLBPowerOfTwoChoices: under Traefik's p2c every
// replica serves, but the slow one gets far fewer requests; wrr over the
// same replicas gives each a third.
func TestIntegrationStarterLBPowerOfTwoChoices(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "lb-p2c", "lb/power-of-two-choices")
	lastLines(t, eng, dir, "p2c-client", "p2c-edge")
	// Labels with the window text: "p2c" alone is in every replica's name.
	ws := lbCompare(t, eng, dir, "p2c-client", 3, 3, "p2c last 5s", "wrr last 5s")
	for _, w := range ws["p2c last 5s"] {
		if s := w.share("p2c-backend-3"); s == 0 || s >= 0.2 {
			t.Errorf("p2c gave the slow replica %.0f%% (want more than none, less than 20%%): %s", s*100, w.line)
		}
	}

	for _, w := range ws["wrr last 5s"] {
		if !w.balanced("p2c-backend", 3, 0.25, 0.42) {
			t.Errorf("wrr did not give each replica a third: %s", w.line)
		}
	}

	t.Logf("%s", ws["p2c last 5s"][2].line)
	t.Logf("%s", ws["wrr last 5s"][2].line)

	routes, err := eng.TraefikRoutes(context.Background(), ExploreRequest{Dir: dir, Service: "p2c-edge"})
	if err != nil {
		t.Fatal(err)
	}

	strategies := map[string]string{}

	for _, s := range routes.Services {
		name := strings.TrimSuffix(s.Name, "@file")

		strategies[name] = s.Strategy
		if name == "p2c" && (s.Upstream.Route != "p2c" || s.Upstream.Target != "p2c-backend") {
			t.Errorf("service p2c is not tagged with the upstream: %+v", s.Upstream)
		}

		for _, srv := range s.Servers {
			if srv.Status != "UP" {
				t.Errorf("service %s: %s is %s", s.Name, srv.Address, srv.Status)
			}
		}
	}

	if strategies["p2c"] != "p2c" || strategies["wrr"] != "wrr" {
		t.Errorf("strategies in the explorer: %v", strategies)
	}
}

// chRound is one round of the consistent-hashing client for one policy.
type chRound struct {
	line                  string
	failed, moved, differ int
	replicas              map[string]int // keys per replica
}

var (
	chRoundRe = regexp.MustCompile(`(ring_hash|maglev) round \d+: 200 keys, (\d+) failed, (\d+) moved, (?:(\d+) differ from baseline|no baseline yet), \d+ cache hits \| (.*)$`)
	chMoveRe  = regexp.MustCompile(`(ring_hash|maglev): (user-\d+) moved (\S+) -> (\S+)`)
)

// chRounds parses the rounds one policy logged from line skip on.
func chRounds(lines []string, policy string, skip int) []chRound {
	var out []chRound

	for _, l := range lines[min(skip, len(lines)):] {
		m := chRoundRe.FindStringSubmatch(l)
		if m == nil || m[1] != policy {
			continue
		}

		r := chRound{line: l, replicas: map[string]int{}}
		r.failed, _ = strconv.Atoi(m[2])
		r.moved, _ = strconv.Atoi(m[3])

		r.differ, _ = strconv.Atoi(m[4])
		for _, f := range strings.Fields(m[5]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				r.replicas[k], _ = strconv.Atoi(v)
			}
		}

		out = append(out, r)
	}

	return out
}

// still reports whether the round answered every key, moved none and
// spread them over n replicas.
func (r chRound) still(n int) bool { return r.failed == 0 && r.moved == 0 && len(r.replicas) == n }

// stillRun returns the index after the first run of n rounds that are all
// still over replicas, or -1.
func stillRun(rs []chRound, replicas, n int) int {
	run := 0

	for i, r := range rs {
		if r.still(replicas) {
			run++
		} else {
			run = 0
		}

		if run == n {
			return i + 1
		}
	}

	return -1
}

// TestIntegrationStarterLBConsistentHashing: through Envoy's ring_hash and
// maglev clusters, each key keeps landing on the same replica; stopping one
// of four replicas moves every key it held and, in all, a minority of keys.
func TestIntegrationStarterLBConsistentHashing(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-ch", "lb/consistent-hashing")
	lastLines(t, eng, dir, "ch-client", "ch-edge")
	envoyConfigValid(t, name, "ch-edge")

	policies := []string{"ring_hash", "maglev"}

	// Once all four replicas are in, six rounds in a row (30s) move no key.
	var before map[string]chRound

	eventually(t, 3*time.Minute, "six rounds in a row with every key in place", func() bool {
		lines := logsOf(t, eng, dir, "ch-client")
		before = map[string]chRound{}

		for _, p := range policies {
			rs := chRounds(lines, p, 0)

			end := stillRun(rs, 4, 6)
			if end < 0 {
				return false
			}

			before[p] = rs[end-1]
		}

		return true
	})

	for _, p := range policies {
		t.Logf("before: %s", before[p].line)
	}

	// The README's walkthrough: stop one replica and count the keys that move.
	skip := len(logsOf(t, eng, dir, "ch-client"))
	docker(t, "stop", containerOf(t, name, "ch-backend-4"))
	eventually(t, 2*time.Minute, "placement to settle on three replicas", func() bool {
		lines := logsOf(t, eng, dir, "ch-client")
		for _, p := range policies {
			if stillRun(chRounds(lines, p, skip), 3, 2) < 0 {
				return false
			}
		}

		return true
	})
	lines := logsOf(t, eng, dir, "ch-client")

	for _, p := range policies {
		moved := map[string]string{} // key -> the replica it left

		for _, l := range lines[skip:] {
			if m := chMoveRe.FindStringSubmatch(l); m != nil && m[1] == p {
				if _, seen := moved[m[2]]; !seen {
					moved[m[2]] = m[3]
				}
			}
		}

		held := before[p].replicas["ch-backend-4"]

		from := map[string]int{}
		for _, r := range moved {
			from[r]++
		}

		t.Logf("%s: ch-backend-4 held %d keys; %d keys moved, from %v", p, held, len(moved), from)

		if len(moved) == 0 || len(moved) >= 100 {
			t.Errorf("%s: %d of 200 keys moved, want a minority", p, len(moved))
		}
		// Every key the stopped replica held moved. Envoy keeps the ring's
		// (and Maglev's table's) size, so the survivors take over its points
		// and may also trade a few keys between them.
		if from["ch-backend-4"] != held {
			t.Errorf("%s: ch-backend-4 held %d keys but %d of them moved", p, held, from["ch-backend-4"])
		}

		rs := chRounds(lines, p, skip)
		t.Logf("after: %s", rs[len(rs)-1].line)
	}

	cl, err := eng.EnvoyClusters(context.Background(), ExploreRequest{Dir: dir, Service: "ch-edge"})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range cl.Clusters {
		if c.Name == "ring-hash" {
			if c.Role != "upstream" || c.Target != "ch-backend" {
				t.Errorf("cluster ring-hash: role %q target %q", c.Role, c.Target)
			}

			return
		}
	}

	t.Errorf("no ring-hash cluster in %+v", cl.Clusters)
}
