//go:build integration

package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// proxyConfigValid runs the proxy's own config check on the file it runs.
func proxyConfigValid(t *testing.T, experiment, service, check string) {
	t.Helper()

	if out, err := execIn(t, experiment, service, check); err != nil {
		t.Fatalf("%s: %s: %v\n%s", service, check, err, out)
	}
}

// TestIntegrationStarterBlueGreen: the client sees only blue, then only
// green, then only blue again, with no failed request across either switch.
// Each switch is the starter's own switch.sh: a smoke test, the switch file
// and NGINX's graceful reload through docker exec. A client that starts
// before blue answers waits for it and counts no failure.
func TestIntegrationStarterBlueGreen(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-bg", "lb/blue-green")
	lastLines(t, eng, dir, "bg-client", "bg-edge")
	proxyConfigValid(t, name, "bg-edge", "nginx -t -q -c /etc/bb/config/nginx.conf")

	// Startup is not a switch. bb up orders only the proxy before the
	// client, so on a busy machine the client's first requests can reach
	// NGINX before bg-blue listens: 502s that NGINX's 5 s DNS cache can
	// stretch over seconds (max_fails does not apply: each colour is a
	// single-server upstream). Force that race on every run: start the
	// client while blue is down, and blue once the client is waiting. The
	// client must keep no score until blue answers.
	blue, client := containerOf(t, name, "bg-blue"), containerOf(t, name, "bg-client")
	// One stop at a time, client first: docker stops several containers in
	// parallel, and blue stopping first would make the client log errors.
	docker(t, "stop", "-t", "1", client)
	docker(t, "stop", "-t", "1", blue)
	skip := len(logsOf(t, eng, dir, "bg-client"))
	docker(t, "start", client)
	eventually(t, time.Minute, "the client to wait for the first answer", func() bool {
		lines := logsOf(t, eng, dir, "bg-client")

		return slices.ContainsFunc(lines[min(skip, len(lines)):], func(l string) bool {
			return strings.Contains(l, "waiting for the first answer: ")
		})
	})
	docker(t, "start", blue)

	window := regexp.MustCompile(`last 2s: (.*) \| total`)
	cutover := regexp.MustCompile(`cutover: (bg-\w+ -> bg-\w+)`)
	// onlyWindows waits for n consecutive tallies, after skip log lines,
	// answered by colour alone.
	onlyWindows := func(colour string, skip int) {
		t.Helper()
		eventually(t, time.Minute, "two tallies answered by "+colour+" only", func() bool {
			lines := logsOf(t, eng, dir, "bg-client")
			ws := matches(lines[min(skip, len(lines)):], window)
			run := 0

			for _, w := range ws {
				if strings.HasPrefix(w, colour+"=") && !strings.Contains(w, " ") {
					run++
				} else {
					run = 0
				}
			}

			return run >= 2
		})
	}
	onlyWindows("bg-blue", skip)

	switchTo := func(colour string) {
		t.Helper()

		out, err := exec.Command("sh", filepath.Join(dir, "blue-green", "switch.sh"), colour).CombinedOutput()
		if err != nil {
			t.Fatalf("switch.sh %s: %v\n%s", colour, err, out)
		}

		t.Logf("switch.sh %s:\n%s", colour, out)
	}
	skip = len(logsOf(t, eng, dir, "bg-client"))

	switchTo("green")
	onlyWindows("bg-green", skip)
	skip = len(logsOf(t, eng, dir, "bg-client"))

	switchTo("blue")
	onlyWindows("bg-blue", skip)

	lines := logsOf(t, eng, dir, "bg-client")
	t.Logf("the client waited %d times for the first answer", len(matches(lines, regexp.MustCompile(`(waiting for the first answer)`))))

	if got := matches(lines, cutover); !slices.Equal(got, []string{"bg-blue -> bg-green", "bg-green -> bg-blue"}) {
		t.Errorf("cutovers = %q, want blue -> green, then green -> blue", got)
	}

	if errs := slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return !strings.Contains(l, "error: ") }); len(errs) > 0 {
		t.Errorf("the client saw failures:\n%s", strings.Join(errs, "\n"))
	}

	totals := matches(lines, regexp.MustCompile(`\| (total .*)`))
	if last := totals[len(totals)-1]; !strings.HasSuffix(last, " errors=0") {
		t.Errorf("last tally: %s", last)
	} else {
		t.Logf("last tally: %s", last)
	}

	if edge := logsOf(t, eng, dir, "bg-edge"); !slices.ContainsFunc(edge, func(l string) bool { return strings.Contains(l, "gracefully shutting down") }) {
		t.Error("bg-edge did not log a graceful worker shutdown")
	}
}

// TestIntegrationStarterStickySessions: each user keeps one replica while
// the anonymous caller rotates; after the replica user-1 sticks to stops,
// HAProxy marks it down and user-1 moves to another replica and stays,
// with its session started over.
func TestIntegrationStarterStickySessions(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-sticky", "lb/sticky-sessions")
	lastLines(t, eng, dir, "sticky-client", "sticky-edge")
	proxyConfigValid(t, name, "sticky-edge", "haproxy -c -q -f /etc/bb/config/haproxy.cfg")

	report := regexp.MustCompile(`(user-1 on sticky-backend-\d visits=\d+ \| .*)`)
	user1 := regexp.MustCompile(`user-1 on (sticky-backend-\d) visits=(\d+)`)
	// where returns user-1's replica in the last n reports after skip lines,
	// or "" if it changed or any report is missing it.
	where := func(skip, n int) string {
		lines := logsOf(t, eng, dir, "sticky-client")

		rs := matches(lines[min(skip, len(lines)):], report)
		if len(rs) < n {
			return ""
		}

		at := ""

		for _, r := range rs[len(rs)-n:] {
			m := user1.FindStringSubmatch(r)
			if m == nil || (at != "" && m[1] != at) {
				return ""
			}

			at = m[1]
		}

		return at
	}

	var sticky string

	eventually(t, 2*time.Minute, "user-1 on one replica for three reports, anonymous calls on all three", func() bool {
		sticky = where(0, 3)
		rs := matches(logsOf(t, eng, dir, "sticky-client"), report)

		return sticky != "" && strings.Contains(rs[len(rs)-1], "no cookie: sticky-backend-1=") &&
			strings.Contains(rs[len(rs)-1], "sticky-backend-2=") && strings.Contains(rs[len(rs)-1], "sticky-backend-3=")
	})
	rs := matches(logsOf(t, eng, dir, "sticky-client"), report)
	t.Logf("before: %s", rs[len(rs)-1])

	skip := len(logsOf(t, eng, dir, "sticky-client"))
	docker(t, "stop", containerOf(t, name, sticky))
	moved := regexp.MustCompile(`user-1 moved: ` + sticky + ` -> (sticky-backend-\d) \(visits=1 there`)

	var to string

	eventually(t, time.Minute, "user-1 to move off "+sticky, func() bool {
		lines := logsOf(t, eng, dir, "sticky-client")

		m := matches(lines[min(skip, len(lines)):], moved)
		if len(m) == 0 {
			return false
		}

		to = m[0]

		return true
	})
	skip = len(logsOf(t, eng, dir, "sticky-client"))
	eventually(t, time.Minute, "user-1 to stay on "+to, func() bool { return where(skip, 2) == to })

	lines := logsOf(t, eng, dir, "sticky-client")
	if n := len(regexp.MustCompile(`user-1 moved`).FindAllString(strings.Join(lines, "\n"), -1)); n != 1 {
		t.Errorf("user-1 moved %d times, want once", n)
	}

	rs = matches(lines, report)
	t.Logf("after %s stopped: %s", sticky, rs[len(rs)-1])

	// The explorer shows the stopped replica out of rotation once its
	// checks have failed.
	eventually(t, 30*time.Second, "the stats page to show "+sticky+" out of rotation", func() bool {
		stats, err := eng.HAProxyStats(context.Background(), ExploreRequest{Dir: dir, Service: "sticky-edge"})
		if err != nil {
			t.Fatal(err)
		}

		for _, b := range stats.Backends {
			for _, s := range b.Servers {
				if s.Name == sticky && !s.Up {
					t.Logf("%s/%s: %s, last check %s", b.Name, s.Name, s.Status, s.CheckStatus)

					return true
				}
			}
		}

		return false
	})
}

// TestIntegrationStarterWeightedCanary: Traefik's weighted service gives v2
// a share within tolerance of 10%, both v1 replicas serve, testers reach v2
// every time with the opt-in header, and no request fails.
func TestIntegrationStarterWeightedCanary(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-canary", "lb/weighted-canary")
	lastLines(t, eng, dir, "canary-client", "canary-edge")

	// Traefik loaded the shipped routers and services, and every server of
	// both versions passes its check.
	eventually(t, time.Minute, "Traefik to load the file with every server up", func() bool {
		routes, err := eng.TraefikRoutes(context.Background(), ExploreRequest{Dir: dir, Service: "canary-edge"})
		if err != nil {
			t.Fatal(err)
		}

		loaded := map[string]string{}
		for _, r := range routes.Routers {
			loaded["router "+r.Name] = r.Status
		}

		for _, s := range routes.Services {
			up := 0

			for _, sv := range s.Servers {
				if sv.Status == "UP" {
					up++
				}
			}

			loaded["service "+s.Name] = fmt.Sprintf("%s %s %d up", s.Type, s.Status, up)
		}

		ok := loaded["router canary@file"] == "enabled" && loaded["router canary-testers@file"] == "enabled" &&
			loaded["service canary@file"] == "weighted enabled 0 up" &&
			loaded["service canary-v1@file"] == "loadbalancer enabled 2 up" && loaded["service canary-v2@file"] == "loadbalancer enabled 1 up"
		if ok {
			t.Logf("Traefik loaded: %v", loaded)
		}

		return ok
	})

	total := regexp.MustCompile(`\| total (\d+): v1=\d+ \(([\d.]+)%\) v2=\d+ \(([\d.]+)%\) errors=(\d+)`)
	window := regexp.MustCompile(`last 5s: (.*) \| total`)

	var last []string

	eventually(t, 2*time.Minute, "1000 requests counted", func() bool {
		lines := logsOf(t, eng, dir, "canary-client")
		for _, l := range slices.Backward(lines) {
			if m := total.FindStringSubmatch(l); m != nil {
				last = m

				break
			}
		}

		return len(last) > 0 && len(last[1]) >= 4
	})
	t.Logf("totals: %s requests, v1 %s%%, v2 %s%%, errors %s", last[1], last[2], last[3], last[4])

	var v2 float64
	if _, err := fmt.Sscan(last[3], &v2); err != nil {
		t.Fatal(err)
	}

	if v2 < 8 || v2 > 12 {
		t.Errorf("v2 share %.1f%%, want 10%% ± 2", v2)
	}

	if last[4] != "0" {
		t.Errorf("%s requests failed", last[4])
	}

	ws := matches(logsOf(t, eng, dir, "canary-client"), window)
	if w := ws[len(ws)-1]; !strings.Contains(w, "canary-v1-1=") || !strings.Contains(w, "canary-v1-2=") || !strings.Contains(w, "canary-v2=") {
		t.Errorf("last window %q lacks a replica", w)
	}

	out, err := execIn(t, name, "canary-client", `for i in 1 2 3 4 5; do wget -qO- --header "X-Canary: always" "$CANARY_EDGE_URL"; done`)
	if err != nil || strings.Count(out, "canary-v2 v2 canary=true") != 5 {
		t.Errorf("testers with the header do not always reach v2: %v\n%s", err, out)
	}
}

// TestIntegrationStarterHealthChecks: one replica fails behind HAProxy
// (active checks) and Envoy (outlier detection only); each proxy removes it,
// the explorers show it out of rotation, and after it recovers each proxy
// restores it. The client's measured times are logged.
func TestIntegrationStarterHealthChecks(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-hc", "lb/health-checks")
	lastLines(t, eng, dir, "hc-client", "hc-backend-3")
	proxyConfigValid(t, name, "hc-active", "haproxy -c -q -f /etc/bb/config/haproxy.cfg")
	envoyConfigValid(t, name, "hc-passive")

	window := regexp.MustCompile(`last 5s: (active: .*)`)

	eventually(t, 2*time.Minute, "every replica answering through both proxies", func() bool {
		ws := matches(logsOf(t, eng, dir, "hc-client"), window)
		if len(ws) == 0 {
			return false
		}

		w := ws[len(ws)-1]

		return strings.Count(w, "hc-backend-3=") == 2 && strings.Count(w, "hc-backend-1=") == 2 && strings.Count(w, "failed=0") == 2
	})

	backend3 := func(action string) {
		t.Helper()

		if out, err := execIn(t, name, "hc-backend-3", "wget -qO- --post-data= http://127.0.0.1:8080/"+action); err != nil {
			t.Fatalf("%s replica 3: %v\n%s", action, err, out)
		}
	}
	skip := len(logsOf(t, eng, dir, "hc-client"))
	since := func(re *regexp.Regexp) []string {
		lines := logsOf(t, eng, dir, "hc-client")

		return matches(lines[min(skip, len(lines)):], re)
	}

	backend3("fail")

	ejected := map[string]*regexp.Regexp{
		"active":  regexp.MustCompile(`active: hc-backend-3 (ejected: .*)`),
		"passive": regexp.MustCompile(`passive: hc-backend-3 (ejected: .*)`),
	}

	eventually(t, time.Minute, "both proxies to eject replica 3", func() bool {
		return len(since(ejected["active"])) > 0 && len(since(ejected["passive"])) > 0
	})
	t.Logf("active %s", since(ejected["active"])[0])
	t.Logf("passive %s", since(ejected["passive"])[0])

	// The explorers agree: HAProxy has the slot down, Envoy has the host
	// ejected (or, between ejections, has ejected it at least once).
	eventually(t, 30*time.Second, "the explorers to show replica 3 out of rotation", func() bool {
		stats, err := eng.HAProxyStats(context.Background(), ExploreRequest{Dir: dir, Service: "hc-active"})
		if err != nil {
			t.Fatal(err)
		}

		down := false

		for _, b := range stats.Backends {
			for _, s := range b.Servers {
				down = down || (strings.HasPrefix(s.Instance, "hc-backend-3") && !s.Up)
			}
		}

		cl, err := eng.EnvoyClusters(context.Background(), ExploreRequest{Dir: dir, Service: "hc-passive"})
		if err != nil {
			t.Fatal(err)
		}

		enforced := false
		for _, c := range cl.Clusters {
			enforced = enforced || (c.Name == "hc-backend" && c.EjectionsEnforced > 0)
		}

		return down && enforced
	})
	// A window in which replica 3 answered through neither proxy.
	eventually(t, 30*time.Second, "a window without replica 3 on the active side", func() bool {
		return slices.ContainsFunc(since(window), func(w string) bool {
			active, _, _ := strings.Cut(w, " | ")

			return !strings.Contains(active, "hc-backend-3")
		})
	})

	skip = len(logsOf(t, eng, dir, "hc-client"))

	backend3("recover")

	back := map[string]*regexp.Regexp{
		"active":  regexp.MustCompile(`active: hc-backend-3 (back: .*)`),
		"passive": regexp.MustCompile(`passive: hc-backend-3 (back: .*)`),
	}
	// Envoy's ejection can last up to 30s by now (third in a row).
	eventually(t, 90*time.Second, "both proxies to restore replica 3", func() bool {
		return len(since(back["active"])) > 0 && len(since(back["passive"])) > 0
	})
	t.Logf("active %s", since(back["active"])[0])
	t.Logf("passive %s", since(back["passive"])[0])
	eventually(t, 30*time.Second, "replica 3 answering through both proxies again", func() bool {
		ws := since(window)

		return len(ws) > 0 && strings.Count(ws[len(ws)-1], "hc-backend-3=") == 2
	})
}
