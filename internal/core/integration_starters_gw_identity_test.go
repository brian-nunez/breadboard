//go:build integration

package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestIntegrationStarterGWJWTAuth: Envoy answers 401 itself to a request
// with no token and to a forged signature, and 403 to a correctly signed
// token issued for another audience; a valid token reaches the
// backend with its claims forwarded as headers, a spoofed claim header
// replaced, and the token stripped.
func TestIntegrationStarterGWJWTAuth(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "gw-jwt", "gw/jwt-auth")
	lastLines(t, eng, dir, "jwt-client", "jwt-backend", "jwt-issuer", "jwt-edge")
	envoyConfigValid(t, name, "jwt-edge")

	want := []*regexp.Regexp{
		regexp.MustCompile(`(no token\s+-> 401 Jwt is missing)`),
		regexp.MustCompile(`(forged signature\s+-> 401 Jwt verification fails)`),
		regexp.MustCompile(`(alice's token for reports-api\s+-> 403 Audiences in Jwt are not allowed)`),
		regexp.MustCompile(`(alice's token\s+-> 200 jwt-backend: orders of sub=alice scope="orders:read orders:write")`),
		regexp.MustCompile(`(bob's token \+ X-Jwt-Sub: admin\s+-> 200 jwt-backend: orders of sub=bob scope="orders:read")`),
	}
	var lines []string
	eventually(t, 3*time.Minute, "a round with every case answered as expected", func() bool {
		lines = logsOf(t, eng, dir, "jwt-client")
		return !slices.ContainsFunc(want, func(re *regexp.Regexp) bool { return len(matches(lines, re)) == 0 })
	})
	for _, re := range want {
		t.Log(matches(lines, re)[0])
	}
	// No unauthenticated or wrong-audience request ever reached the backend.
	for _, l := range lines {
		if (strings.Contains(l, "no token") || strings.Contains(l, "forged signature") || strings.Contains(l, "for reports-api")) && strings.Contains(l, "-> 200") {
			t.Errorf("an unauthenticated request succeeded: %s", l)
		}
	}
	backend := logsOf(t, eng, dir, "jwt-backend")
	seen := regexp.MustCompile(`(GET /api/orders for sub=(\S+) scope="[^"]*" \(values of X-Jwt-Sub: (\d+); Authorization forwarded: (\w+)\))`)
	n := 0
	for _, l := range backend {
		m := seen.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n++
		if m[2] != "alice" && m[2] != "bob" || m[3] != "1" || m[4] != "false" {
			t.Errorf("the backend saw %s", m[1])
		}
	}
	if n == 0 {
		t.Fatal("the backend logged no request")
	}
	if !slices.ContainsFunc(logsOf(t, eng, dir, "jwt-issuer"), func(l string) bool { return strings.Contains(l, "served the JWKS") }) {
		t.Error("the issuer never served its JWKS")
	}
	stats := envoyAdmin(t, name, "jwt-edge", "/stats?filter=jwt_authn")
	t.Logf("jwt-edge stats:\n%s", stats)
	for _, s := range []string{"jwt_authn.allowed", "jwt_authn.denied", "jwt_authn.jwks_fetch_success"} {
		if !regexp.MustCompile(regexp.QuoteMeta(s) + `: [1-9]`).MatchString(stats) {
			t.Errorf("jwt-edge counted no %s", s)
		}
	}
	t.Logf("the backend logged %d verified requests, each with one X-Jwt-Sub and no token", n)
}

// TestIntegrationStarterGWForwardAuth: Traefik loads the routers with both
// middlewares; the auth service's 401s and 403 reach the client unchanged;
// an allowed request reaches the backend with the identity the auth service
// returned (a spoofed X-Auth-User replaced) and without the token; a
// revoked session is refused on the next request.
func TestIntegrationStarterGWForwardAuth(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "gw-fauth", "gw/forward-auth")
	lastLines(t, eng, dir, "fauth-client", "fauth-backend", "fauth-auth", "fauth-edge")

	eventually(t, time.Minute, "Traefik to load the routers and middlewares with every server up", func() bool {
		routes, err := eng.TraefikRoutes(context.Background(), ExploreRequest{Dir: dir, Service: "fauth-edge"})
		if err != nil {
			t.Fatal(err)
		}
		loaded := map[string]string{}
		for _, r := range routes.Routers {
			loaded["router "+r.Name] = r.Status + " " + strings.Join(r.Middlewares, ",")
		}
		for _, s := range routes.Services {
			up := 0
			for _, sv := range s.Servers {
				if sv.Status == "UP" {
					up++
				}
			}
			loaded["service "+s.Name] = fmt.Sprintf("%s %d up", s.Status, up)
		}
		ok := loaded["router auth@file"] == "enabled " &&
			loaded["router api@file"] == "enabled check-session@file,drop-credentials@file" &&
			loaded["service auth@file"] == "enabled 1 up" && loaded["service backend@file"] == "enabled 1 up"
		if ok {
			t.Logf("Traefik loaded: %v", loaded)
		}
		return ok
	})

	want := []*regexp.Regexp{
		regexp.MustCompile(`(no token\s+-> 401 fauth-auth: no credentials)`),
		regexp.MustCompile(`(made-up token\s+-> 401 fauth-auth: unknown or revoked session)`),
		regexp.MustCompile(`(alice's session\s+-> 200 fauth-backend: orders for user=alice roles=admin,orders)`),
		regexp.MustCompile(`(bob's session \+ X-Auth-User: admin\s+-> 200 fauth-backend: orders for user=bob roles=orders)`),
		regexp.MustCompile(`(bob's session on /api/admin/\s+-> 403 fauth-auth: bob lacks role admin)`),
		regexp.MustCompile(`(bob's session after logout\s+-> 401 fauth-auth: unknown or revoked session)`),
	}
	var lines []string
	eventually(t, 3*time.Minute, "a round with every case answered as expected", func() bool {
		lines = logsOf(t, eng, dir, "fauth-client")
		return !slices.ContainsFunc(want, func(re *regexp.Regexp) bool { return len(matches(lines, re)) == 0 })
	})
	for _, re := range want {
		t.Log(matches(lines, re)[0])
	}
	for _, l := range lines {
		for _, c := range []string{"no token", "made-up token", "on /api/admin/", "after logout"} {
			if strings.Contains(l, c) && strings.Contains(l, "-> 200") {
				t.Errorf("a refused request succeeded: %s", l)
			}
		}
	}

	seen := regexp.MustCompile(`(GET (\S+) for user=(\S*) roles=\S* \(values of X-Auth-User: (\d+); Authorization forwarded: (\w+)\))`)
	n := 0
	for _, l := range logsOf(t, eng, dir, "fauth-backend") {
		m := seen.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n++
		if m[3] != "alice" && m[3] != "bob" || m[4] != "1" || m[5] != "false" || m[2] == "/api/admin/report" && m[3] != "alice" {
			t.Errorf("the backend saw %s", m[1])
		}
	}
	if n == 0 {
		t.Fatal("the backend logged no request")
	}
	auth := logsOf(t, eng, dir, "fauth-auth")
	for _, s := range []string{"allowed GET /api/orders for alice", "denied GET /api/admin/report for bob: 403", "revoked a session of bob"} {
		if !slices.ContainsFunc(auth, func(l string) bool { return strings.Contains(l, s) }) {
			t.Errorf("the auth service never logged %q", s)
		}
	}
	t.Logf("the backend logged %d allowed requests, each with one X-Auth-User and no token", n)
}
