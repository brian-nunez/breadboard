//go:build integration

package core

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// burstSample is one burst of the gw/rate-limiting client through one
// gateway: how many of its requests were accepted and rejected, and the
// slowest accepted answer.
type burstSample struct {
	gateway, client    string
	accepted, rejected int
	slowest            time.Duration
	line               string
}

var burstLine = regexp.MustCompile(`\[(token|leaky)\] (alice|bob) burst of 30: (.*?) \| accepted after \S+?\.\.(\S+) \| rejected after`)

func parseBursts(lines []string) []burstSample {
	var out []burstSample
	for _, l := range lines {
		m := burstLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		s := burstSample{gateway: m[1], client: m[2], line: l[strings.Index(l, "["):]}
		for _, kv := range strings.Fields(m[3]) {
			k, v, _ := strings.Cut(kv, "=")
			n, _ := strconv.Atoi(v)
			switch k {
			case "200":
				s.accepted = n
			case "429":
				s.rejected = n
			}
		}
		s.slowest, _ = time.ParseDuration(m[4])
		out = append(out, s)
	}
	return out
}

// TestIntegrationStarterRateLimiting: under the same bursts, Envoy's token
// bucket accepts its 10 tokens at once and rejects the rest, and NGINX's
// leaky bucket accepts one request plus its queue of 10, delaying the
// queued ones to its rate; each client has its own bucket, and a client
// under the rate is never limited.
func TestIntegrationStarterRateLimiting(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "gw-rate", "gw/rate-limiting")
	lastLines(t, eng, dir, "rate-client", "rate-backend", "rate-token", "rate-leaky")
	envoyConfigValid(t, name, "rate-token")
	if out, err := execIn(t, name, "rate-leaky", "nginx -t 2>&1"); err != nil {
		t.Fatalf("nginx -t: %v\n%s", err, out)
	}

	// Enough samples per gateway and client that one burst disturbed by a
	// busy host cannot decide the result.
	const want = 4
	var bursts []burstSample
	eventually(t, 4*time.Minute, "4 bursts per gateway and client", func() bool {
		bursts = parseBursts(logsOf(t, eng, dir, "rate-client"))
		for _, g := range []string{"token", "leaky"} {
			for _, c := range []string{"alice", "bob"} {
				if n := len(slices.DeleteFunc(slices.Clone(bursts), func(s burstSample) bool { return s.gateway != g || s.client != c })); n < want {
					return false
				}
			}
		}
		return true
	})
	for _, s := range bursts {
		t.Log(s.line)
	}

	// The expected accepted counts follow from each config: the token
	// bucket holds 10 tokens; the leaky bucket forwards 1 and queues 10.
	// Requests of a burst arrive over a few milliseconds, so the bucket
	// can gain a token (refilled 5/s) or leak a slot (5/s) while they
	// arrive: allow 2 more. Every request is accepted or rejected.
	expect := map[string][2]int{"token": {10, 12}, "leaky": {11, 13}}
	for _, s := range bursts {
		lo, hi := expect[s.gateway][0], expect[s.gateway][1]
		if s.accepted < lo || s.accepted > hi || s.accepted+s.rejected != 30 {
			t.Errorf("%s: accepted %d, rejected %d; want %d to %d accepted and the rest rejected", s.line, s.accepted, s.rejected, lo, hi)
		}
	}

	// Token bucket: the accepted part of a burst is forwarded at once.
	// Leaky bucket: the queued part leaves at 5 a second, so the last of
	// 10 queued requests waits about 2s. Medians keep one slow sample on
	// a shared host from deciding.
	median := func(g string) time.Duration {
		var d []time.Duration
		for _, s := range bursts {
			if s.gateway == g {
				d = append(d, s.slowest)
			}
		}
		slices.Sort(d)
		return d[len(d)/2]
	}
	if token := median("token"); token > 700*time.Millisecond {
		t.Errorf("token bucket: the slowest accepted request of a burst took %s (median); want it forwarded at once", token)
	}
	if leaky := median("leaky"); leaky < 1500*time.Millisecond || leaky > 4*time.Second {
		t.Errorf("leaky bucket: the slowest accepted request of a burst took %s (median); want about 2s (10 queued at 5/s)", leaky)
	}

	// Under the rate, carol is never limited by either gateway.
	steady := regexp.MustCompile(`(\[(token|leaky)\] carol steady at 4/s: (\d+) requests, (.*?) \|.*)`)
	var lines []string
	eventually(t, time.Minute, "steady windows from both gateways", func() bool {
		lines = matches(logsOf(t, eng, dir, "rate-client"), steady)
		return slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "[token]") }) &&
			slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "[leaky]") })
	})
	for _, l := range lines {
		if strings.Contains(l, "429=") || strings.Contains(l, "error=") {
			t.Errorf("carol, under the limit, was limited: %s", l)
		}
	}
	t.Log(lines[len(lines)-1])

	// Rejected requests never reach the backend: per 10s window it receives
	// at most what the gateways accepted. Each window holds one burst per
	// client (accepted at most 12 or 13) plus carol's 40.
	received := regexp.MustCompile(`via (rate-token|rate-leaky): ([^|]*)`)
	for _, l := range logsOf(t, eng, dir, "rate-backend") {
		for _, m := range received.FindAllStringSubmatch(l, -1) {
			for _, kv := range strings.Fields(m[2]) {
				k, v, _ := strings.Cut(kv, "=")
				n, _ := strconv.Atoi(v)
				if (k == "alice" || k == "bob") && n > 13 {
					t.Errorf("rate-backend received %d requests from %s via %s in one window: %s", n, k, m[1], l)
				}
			}
		}
	}

	// Envoy's own counters agree that it rejected requests.
	if n := envoyStat(t, name, "rate-token", "rate_token.http_local_rate_limit.enforced"); n == 0 {
		t.Error("rate-token enforced no rate limit")
	} else {
		t.Logf("rate-token rate_token.http_local_rate_limit.enforced: %d", n)
	}
}

// TestIntegrationStarterCircuitBreaking: under four times the load Envoy's
// circuit breaker admits, the excess fails at once with 503 and
// x-envoy-overloaded, and the backend never serves more requests at once
// than max_requests.
func TestIntegrationStarterCircuitBreaking(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "gw-cb", "gw/circuit-breaking")
	lastLines(t, eng, dir, "cb-client", "cb-backend", "cb-edge")
	envoyConfigValid(t, name, "cb-edge")

	window := regexp.MustCompile(`last 5s: served (\d+) \(p50 (\S+), p99 (\S+)\) \| overflow 503 (\d+) \(p50 (\S+), p99 (\S+)\)(.*)`)
	type win struct {
		served, overflow       int
		servedP50, overflowP50 time.Duration
		overflowP99            time.Duration
		other, line            string
	}
	var wins []win
	eventually(t, 3*time.Minute, "6 client windows with served and refused requests", func() bool {
		wins = nil
		for _, l := range logsOf(t, eng, dir, "cb-client") {
			m := window.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			w := win{other: m[7], line: m[0]}
			w.served, _ = strconv.Atoi(m[1])
			w.overflow, _ = strconv.Atoi(m[4])
			w.servedP50, _ = time.ParseDuration(m[2])
			w.overflowP50, _ = time.ParseDuration(m[5])
			w.overflowP99, _ = time.ParseDuration(m[6])
			if w.served > 0 && w.overflow > 0 {
				wins = append(wins, w)
			}
		}
		return len(wins) >= 6
	})
	// The first window can hold the start-up; judge the rest.
	wins = wins[1:]
	var servedP50, overflowP50, overflowP99 []time.Duration
	for _, w := range wins {
		t.Log(w.line)
		if w.other != "" {
			t.Errorf("a request failed other than by the breaker: %s", w.line)
		}
		servedP50 = append(servedP50, w.servedP50)
		overflowP50 = append(overflowP50, w.overflowP50)
		overflowP99 = append(overflowP99, w.overflowP99)
	}
	med := func(d []time.Duration) time.Duration { slices.Sort(d); return d[len(d)/2] }
	// A served request costs the backend at least 200ms; a refused one
	// never reaches it. Medians over windows keep one slow moment on a
	// shared host from deciding.
	if m := med(servedP50); m < 200*time.Millisecond {
		t.Errorf("served requests took %s (median p50); the backend takes at least 200ms", m)
	}
	if m := med(overflowP50); m > 50*time.Millisecond {
		t.Errorf("refused requests took %s (median p50); want an immediate 503", m)
	}
	if m := med(overflowP99); m >= 200*time.Millisecond {
		t.Errorf("refused requests took %s (median p99); want less than one backend request (200ms)", m)
	}

	// The backend's own measure: never more than max_requests (5) at once,
	// and the limit is reached, so the breaker, not the client, set it.
	peak := regexp.MustCompile(`in flight peak (\d+) \(since start (\d+)\)`)
	var ever int
	eventually(t, time.Minute, "a backend window", func() bool {
		for _, m := range peak.FindAllStringSubmatch(strings.Join(logsOf(t, eng, dir, "cb-backend"), "\n"), -1) {
			n, _ := strconv.Atoi(m[2])
			ever = max(ever, n)
		}
		return ever > 0
	})
	if ever != 5 {
		t.Errorf("cb-backend served at most %d requests at once; want exactly max_requests, 5", ever)
	}
	t.Logf("cb-backend peak concurrency since start: %d", ever)

	pending := envoyStat(t, name, "cb-edge", "cluster.cb-backend.upstream_rq_pending_overflow")
	active := envoyStat(t, name, "cb-edge", "cluster.cb-backend.upstream_rq_active_overflow")
	if pending+active == 0 {
		t.Error("cb-edge counted no circuit breaker overflow")
	}
	t.Logf("cb-edge overflows: pending %d, active %d", pending, active)
}

// TestIntegrationStarterRetriesAndTimeouts: against a backend that fails 4
// tries in 10, Envoy's retries with a per-try timeout raise the success
// rate from about 60% to nearly 100%, and the route timeout cuts stuck
// answers short; against a backend that is down, the same retries
// multiply its load by about 4 without a budget, and a retry budget keeps
// it bounded.
func TestIntegrationStarterRetriesAndTimeouts(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "gw-retry", "gw/retries-and-timeouts")
	lastLines(t, eng, dir, "retry-client", "retry-flaky", "retry-down", "retry-edge")
	envoyConfigValid(t, name, "retry-edge")

	// The client's 5s windows, per route.
	type win struct {
		ok       int
		attempts float64
		p99      time.Duration
		line     string
	}
	tally := regexp.MustCompile(`\[(\S+)\] (\d+) requests: .*? \| ok (\d+)% \| ([\d.]+) attempts per request \| p50 \S+ p99 (\S+)`)
	routes := []string{"flaky/no-retries", "flaky/retries", "down/no-budget", "down/budget"}
	// Enough windows per route that one disturbed by a busy host cannot
	// decide; the first, which holds the start-up, is skipped.
	const want = 6
	var wins map[string][]win
	eventually(t, 4*time.Minute, "6 client windows per route", func() bool {
		wins = map[string][]win{}
		for _, l := range logsOf(t, eng, dir, "retry-client") {
			m := tally.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			w := win{line: m[0]}
			w.ok, _ = strconv.Atoi(m[3])
			w.attempts, _ = strconv.ParseFloat(m[4], 64)
			w.p99, _ = time.ParseDuration(m[5])
			wins[m[1]] = append(wins[m[1]], w)
		}
		for _, r := range routes {
			if len(wins[r]) < want+1 {
				return false
			}
		}
		return true
	})
	medianOf := func(r string, f func(w win) float64) float64 {
		var v []float64
		for _, w := range wins[r][1:] {
			v = append(v, f(w))
		}
		slices.Sort(v)
		return v[len(v)/2]
	}
	for _, r := range routes {
		t.Log(wins[r][len(wins[r])-1].line)
	}

	// Retries hide a flaky backend's failures: 60% expected without them,
	// about 97% with 4 tries (0.4^4 of requests fail every try).
	noRetries := medianOf("flaky/no-retries", func(w win) float64 { return float64(w.ok) })
	retries := medianOf("flaky/retries", func(w win) float64 { return float64(w.ok) })
	if noRetries < 45 || noRetries > 75 {
		t.Errorf("flaky/no-retries: %v%% ok (median); want about 60%%", noRetries)
	}
	if retries < 90 {
		t.Errorf("flaky/retries: %v%% ok (median); want at least 90%%", retries)
	}
	if a := medianOf("flaky/no-retries", func(w win) float64 { return w.attempts }); a != 1 {
		t.Errorf("flaky/no-retries: %.2f attempts per request (median); want exactly 1", a)
	}
	if a := medianOf("flaky/retries", func(w win) float64 { return w.attempts }); a < 1.2 || a > 2 {
		t.Errorf("flaky/retries: %.2f attempts per request (median); want about 1.6", a)
	}
	// The route timeout answers a stuck request after 1s, not the
	// backend's 3s; the per-try timeout lets a retry finish well within
	// the 2s deadline.
	if p := time.Duration(medianOf("flaky/no-retries", func(w win) float64 { return float64(w.p99) })); p < 900*time.Millisecond || p > 1500*time.Millisecond {
		t.Errorf("flaky/no-retries: p99 %s (median); want about the 1s route timeout", p)
	}
	if p := time.Duration(medianOf("flaky/retries", func(w win) float64 { return float64(w.p99) })); p > 1500*time.Millisecond {
		t.Errorf("flaky/retries: p99 %s (median); want well under the 2s deadline", p)
	}

	// The storm, seen by retry-down: tries received per client request.
	storm := regexp.MustCompile(`(/down/(?:no-budget|budget)) (\d+) attempts for (\d+) requests`)
	ratios := map[string][]float64{}
	eventually(t, time.Minute, "6 retry-down windows per route", func() bool {
		ratios = map[string][]float64{}
		lines := logsOf(t, eng, dir, "retry-down")
		for _, l := range lines[min(1, len(lines)):] {
			for _, m := range storm.FindAllStringSubmatch(l, -1) {
				a, _ := strconv.Atoi(m[2])
				r, _ := strconv.Atoi(m[3])
				if r > 0 {
					ratios[m[1]] = append(ratios[m[1]], float64(a)/float64(r))
				}
			}
		}
		return len(ratios["/down/no-budget"]) >= want && len(ratios["/down/budget"]) >= want
	})
	med := func(v []float64) float64 { v = slices.Clone(v); slices.Sort(v); return v[len(v)/2] }
	noBudget, budget := med(ratios["/down/no-budget"]), med(ratios["/down/budget"])
	t.Logf("retry-down tries per request (median): no budget %.2f, budget %.2f", noBudget, budget)
	// Without a budget every failed try is retried: 4 tries per request.
	if noBudget < 3.5 {
		t.Errorf("down/no-budget: %.2f tries per request (median); want about 4", noBudget)
	}
	// With a 20% budget (at least 1 retry in flight), retries stay a
	// fraction of the load.
	if budget > 2 {
		t.Errorf("down/budget: %.2f tries per request (median); want the budget to keep it under 2", budget)
	}
	if n := envoyStat(t, name, "retry-edge", "cluster.retry-down-budget.upstream_rq_retry_overflow"); n == 0 {
		t.Error("retry-edge counted no retry refused by the budget")
	} else {
		t.Logf("retry-edge cluster.retry-down-budget.upstream_rq_retry_overflow: %d", n)
	}
	if n := envoyStat(t, name, "retry-edge", "cluster.retry-down-no-budget.upstream_rq_retry_overflow"); n != 0 {
		t.Errorf("retry-edge refused %d retries on the cluster without a budget", n)
	}
}
