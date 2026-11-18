//go:build integration

package core

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestIntegrationStarterAPIVersioning: through NGINX, v1 and v2 answer from
// their own backends whether chosen by path or by Api-Version header, an
// unsupported version is refused by the gateway, and every v1 answer (and
// only v1's) carries the Deprecation, Sunset and Link headers.
func TestIntegrationStarterAPIVersioning(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "gw-ver", "gw/api-versioning")
	lastLines(t, eng, dir, "ver-client", "ver-edge")
	proxyConfigValid(t, name, "ver-edge", "nginx -t -q -c /etc/bb/config/nginx.conf")

	end := regexp.MustCompile(`round \d+: 6 requests, unexpected 0$`)

	var (
		round   []string
		summary string
	)

	eventually(t, 2*time.Minute, "a round with every version answered as expected", func() bool {
		round, summary = lastRound(logsOf(t, eng, dir, "ver-client"), end, 6)

		return round != nil
	})

	const deprecated = `deprecated since 2026-01-01 \(Deprecation: @1767225600\), sunset Thu, 01 Jul 2027 00:00:00 GMT, link </v2/>; rel="successor-version"`

	v1 := `ver-v1 200 ` + deprecated + ` \| \{"id":7,"total":"87\.50"\}$`
	v2 := `ver-v2 200 current \| \{"id":7,"total":\{"amount":8750,"currency":"USD"\}\}$`

	want := []*regexp.Regexp{
		regexp.MustCompile(`GET /v1/orders/7 \(Api-Version: -\) -> ` + v1),
		regexp.MustCompile(`GET /v2/orders/7 \(Api-Version: -\) -> ` + v2),
		regexp.MustCompile(`GET /orders/7 \(Api-Version: 1\) -> ` + v1),
		regexp.MustCompile(`GET /orders/7 \(Api-Version: 2\) -> ` + v2),
		regexp.MustCompile(`GET /orders/7 \(Api-Version: -\) -> ` + v1),
		regexp.MustCompile(`GET /orders/7 \(Api-Version: 3\) -> gateway 400 current \| unsupported Api-Version "3": use 1 or 2$`),
	}
	for i, re := range want {
		if !re.MatchString(round[i]) {
			t.Errorf("request %d: %q does not match %s", i+1, round[i], re)
		}
	}

	t.Logf("%s:\n%s", summary, strings.Join(round, "\n"))

	// Each backend saw only its own version's requests, without the
	// version in the path.
	for app, other := range map[string]string{"ver-v1": `"2"`, "ver-v2": `"1"`} {
		lines := logsOf(t, eng, dir, app)
		got := 0

		for _, l := range lines {
			if !strings.Contains(l, "GET /orders/7") {
				continue
			}

			got++

			if strings.Contains(l, "Api-Version "+other) {
				t.Errorf("%s received another version's request: %s", app, l)
			}
		}

		if got == 0 {
			t.Errorf("%s logged no GET /orders/7", app)
		}
	}

	if edge := logsOf(t, eng, dir, "ver-edge"); !regexp.MustCompile(`"GET /orders/7 HTTP/2\.0" 200 api_version=v2 header="2"`).MatchString(strings.Join(edge, "\n")) {
		t.Error("ver-edge's access log has no header-routed v2 request")
	}
}
