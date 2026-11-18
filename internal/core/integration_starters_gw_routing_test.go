//go:build integration

package core

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// traefikRoutesLoaded checks, through the Traefik explorer, that every named
// router of the learner's file loaded without an error and every server of
// every service is UP. Traefik loading the routers is its config check.
func traefikRoutesLoaded(t *testing.T, eng *Engine, dir, service string, routers ...string) *TraefikRoutes {
	t.Helper()

	var routes *TraefikRoutes

	eventually(t, time.Minute, service+"'s routers and servers", func() bool {
		var err error

		routes, err = eng.TraefikRoutes(context.Background(), ExploreRequest{Dir: dir, Service: service})
		if err != nil {
			t.Logf("traefik routes: %v", err)

			return false
		}

		for _, s := range routes.Services {
			for _, srv := range s.Servers {
				if srv.Status != "UP" {
					return false
				}
			}
		}

		return true
	})

	loaded := map[string]TraefikRouter{}
	for _, r := range routes.Routers {
		loaded[strings.TrimSuffix(r.Name, "@file")] = r
		if r.Status != "enabled" || len(r.Errors) > 0 {
			t.Errorf("%s: router %s is %s: %v", service, r.Name, r.Status, r.Errors)
		}
	}

	for _, name := range routers {
		if _, ok := loaded[name]; !ok {
			t.Errorf("%s: router %s not loaded; have %v", service, name, routes.Routers)
		}
	}

	return routes
}

// lastRound returns the request lines of the latest complete client round
// that ends with a line matching end, or nil.
func lastRound(lines []string, end *regexp.Regexp, n int) (round []string, summary string) {
	for i := len(lines) - 1; i >= n; i-- {
		if end.MatchString(lines[i]) {
			return lines[i-n : i], lines[i]
		}
	}

	return nil, ""
}

// TestIntegrationStarterPathHostRouting: through one Traefik, each path
// route and the Host route reach their own service (with /users stripped
// and /orders forwarded as is), and unknown paths, /usersettings included,
// get the default route's 404.
func TestIntegrationStarterPathHostRouting(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "gw-route", "gw/path-host-routing")
	lastLines(t, eng, dir, "route-client", "route-edge")

	routes := traefikRoutesLoaded(t, eng, dir, "route-edge", "users", "orders", "admin", "default")
	for _, s := range routes.Services {
		if name := strings.TrimSuffix(s.Name, "@file"); s.Upstream.Target != name {
			t.Errorf("Traefik service %s is not tagged with its upstream: %+v", s.Name, s.Upstream)
		}
	}

	end := regexp.MustCompile(`round \d+: 7 requests, misrouted 0$`)

	var (
		round   []string
		summary string
	)

	eventually(t, 2*time.Minute, "a round with every request on its route", func() bool {
		round, summary = lastRound(logsOf(t, eng, dir, "route-client"), end, 7)

		return round != nil
	})

	want := []*regexp.Regexp{
		regexp.MustCompile(`GET /users/42 \(host route-edge\) -> route-users 200: path=/42 prefix=/users host=route-edge\.\S+ \| user 42$`),
		regexp.MustCompile(`GET /users \(host route-edge\) -> route-users 200: path=/ prefix=/users .*\| users: `),
		regexp.MustCompile(`GET /orders/7 \(host route-edge\) -> route-orders 200: path=/orders/7 prefix=- .*\| order 7$`),
		regexp.MustCompile(`GET / \(host admin\.route-edge\.\S+\) -> route-admin 200: path=/ prefix=- host=admin\.route-edge\.`),
		regexp.MustCompile(`GET /users/42 \(host admin\.route-edge\.\S+\) -> route-admin 200: path=/users/42 `),
		regexp.MustCompile(`GET /usersettings \(host route-edge\) -> route-default 404: path=/usersettings .*\| no route for /usersettings$`),
		regexp.MustCompile(`GET /nope \(host route-edge\) -> route-default 404: path=/nope .*\| no route for /nope$`),
	}
	for i, re := range want {
		if !re.MatchString(round[i]) {
			t.Errorf("request %d: %q does not match %s", i+1, round[i], re)
		}
	}

	t.Logf("%s:\n%s", summary, strings.Join(round, "\n"))

	if lines := logsOf(t, eng, dir, "route-default"); !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "no route: GET /nope") }) {
		t.Error("route-default did not log the unknown path")
	}

	if lines := logsOf(t, eng, dir, "route-users"); slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "GET /users/") }) {
		t.Error("route-users received an unstripped /users/ path")
	}
}

// TestIntegrationStarterRequestTransformation: through Traefik's middlewares
// the backend sees the added X-Gateway header, the stripped and the
// rewritten paths with the headers that record them, and never the removed
// X-User-Id; responses lose X-Powered-By and gain X-Gateway; a retired URL
// is a permanent redirect the backend never sees.
func TestIntegrationStarterRequestTransformation(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "gw-xform", "gw/request-transformation")
	lastLines(t, eng, dir, "xform-client", "xform-edge", "xform-backend")
	traefikRoutesLoaded(t, eng, dir, "xform-edge", "api", "legacy", "v0")

	end := regexp.MustCompile(`round \d+: 3 requests, unexpected 0$`)

	var (
		round   []string
		summary string
	)

	eventually(t, 2*time.Minute, "a round with every transformation as expected", func() bool {
		round, summary = lastRound(logsOf(t, eng, dir, "xform-client"), end, 3)

		return round != nil
	})

	want := []*regexp.Regexp{
		regexp.MustCompile(`GET /api/orders/7 \(sent X-User-Id: admin, .*X-Request-Id: ([0-9a-f]{16})\) -> 200 \| backend saw path=/orders/7 gateway=xform-edge user-id=- request-id=([0-9a-f]{16}) prefix=/api replaced=- forwarded-for=\S+ .*\| response gateway=xform-edge powered-by=-$`),
		regexp.MustCompile(`GET /legacy/orders/7 -> 200 \| backend saw path=/orders/7 gateway=xform-edge user-id=- .*replaced=/legacy/orders/7 .*\| response gateway=xform-edge powered-by=-$`),
		regexp.MustCompile(`GET /v0/orders/7 -> 30[18] Location: https://xform-edge\.\S+:8443/api/orders/7 -> 200 \| backend saw path=/orders/7$`),
	}
	for i, re := range want {
		if !re.MatchString(round[i]) {
			t.Errorf("request %d: %q does not match %s", i+1, round[i], re)
		}
	}

	if m := want[0].FindStringSubmatch(round[0]); m != nil && m[1] != m[2] {
		t.Errorf("request ID sent %s, seen %s", m[1], m[2])
	}

	t.Logf("%s:\n%s", summary, strings.Join(round, "\n"))

	// The backend's own log: what it received, request by request.
	lines := logsOf(t, eng, dir, "xform-backend")

	saw := slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return !strings.Contains(l, "saw GET") })
	if len(saw) == 0 {
		t.Fatal("xform-backend logged no request")
	}

	for _, l := range saw {
		if !strings.Contains(l, "user-id=-") || strings.Contains(l, "admin") {
			t.Errorf("the removed X-User-Id reached the backend: %s", l)
		}

		if strings.Contains(l, "203.0.113.7") {
			t.Errorf("the forged X-Forwarded-For reached the backend: %s", l)
		}

		if strings.Contains(l, "path=/api/") || strings.Contains(l, "path=/legacy/") || strings.Contains(l, "path=/v0/") {
			t.Errorf("the backend saw an untransformed path: %s", l)
		}
	}

	t.Logf("xform-backend: %s", saw[len(saw)-1])
}
