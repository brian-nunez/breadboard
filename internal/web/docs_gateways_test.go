package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/starters"
)

// gatewayStarters are the registered wave D starters, the gw/ area.
func gatewayStarters() []string {
	var ids []string
	for _, st := range starters.All() {
		if strings.HasPrefix(st.ID, "gw/") {
			ids = append(ids, st.ID)
		}
	}
	return ids
}

// Every API gateway page is in the search index.
func TestGatewayDocsAreInSearch(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	index := s.get(t, "/search-index", cookie).Body.String()
	tc, ok := findTech("api-gateways")
	if !ok {
		t.Fatal("docs section api-gateways is not registered")
	}
	if len(tc.Guides) != 7 {
		t.Errorf("api-gateways has %d guides, want the 7 of the wave D design", len(tc.Guides))
	}
	for _, url := range append([]string{"/docs/api-gateways"}, guideURLs(tc)...) {
		if !strings.Contains(index, `"`+url+`"`) {
			t.Errorf("search has no %s", url)
		}
	}
}

// Every gateway guide has a diagram, links its starters, and ends with the
// official docs for the pinned proxy versions.
func TestGatewayGuidesLinkStartersAndDocs(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	tc, _ := findTech("api-gateways")
	gw := gatewayStarters()
	if len(gw) != 10 {
		t.Errorf("%d gw/ starters are registered, want the 10 of the wave D design", len(gw))
	}
	linked := map[string]bool{}
	for _, url := range guideURLs(tc) {
		rec := s.get(t, url, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", url, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `href="/starters/gw/`) {
			t.Errorf("%s links no gateway starter", url)
		}
		if strings.Contains(body, "@doc") {
			t.Errorf("%s renders a component call as text", url)
		}
		if !strings.Contains(body, "<svg") {
			t.Errorf("%s has no diagram", url)
		}
		if !strings.Contains(body, `id="official"`) || !regexp.MustCompile(`href="https://(www\.envoyproxy\.io/docs/envoy/v1\.40|doc\.traefik\.io/traefik/v3\.7/|nginx\.org/en/docs/)`).MatchString(body) {
			t.Errorf("%s does not end with official docs for the pinned proxies", url)
		}
		for _, id := range gw {
			if strings.Contains(body, `href="/starters/`+id+`"`) {
				linked[id] = true
			}
		}
	}
	for _, id := range gw {
		if !linked[id] {
			t.Errorf("no gateway guide links %s", id)
		}
	}
	official := s.get(t, "/docs/api-gateways", cookie).Body.String()
	for _, version := range []string{"envoy/v1.40", "traefik/v3.7", "nginx.org/en/docs/"} {
		if !strings.Contains(official, version) {
			t.Errorf("/docs/api-gateways links no official docs for %s", version)
		}
	}
}

// The rate-limiting guide covers the four algorithms and local against
// global limiting, and says Breadboard's examples limit locally.
func TestRateLimitGuideCoversAlgorithms(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	body := s.get(t, "/docs/api-gateways/rate-limiting", cookie).Body.String()
	for _, want := range []string{
		`id="token-bucket"`, `id="leaky-bucket"`, `id="fixed-window"`, `id="sliding-window"`, `id="local-global"`,
		"Breadboard&#39;s examples use local limiting", "twice the limit",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rate-limiting guide lacks %q", want)
		}
	}
}

// Each proxy section lists the wave D starters that run on it.
func TestProxySectionsListGatewayStarters(t *testing.T) {
	for id, want := range map[string][]string{
		"envoy":   {"gw/rate-limiting", "gw/circuit-breaking", "gw/retries-and-timeouts", "gw/jwt-auth", "gw/grpc-routing"},
		"traefik": {"gw/path-host-routing", "gw/request-transformation", "gw/forward-auth", "gw/bff-aggregation"},
		"nginx":   {"gw/api-versioning", "gw/rate-limiting"},
	} {
		tc, _ := findTech(id)
		for _, st := range want {
			if !strings.Contains(strings.Join(tc.Starters, " "), st) {
				t.Errorf("/docs/%s does not list %s", id, st)
			}
		}
	}
}

func guideURLs(tc tech) []string {
	urls := make([]string, len(tc.Guides))
	for i, g := range tc.Guides {
		urls[i] = "/docs/" + tc.ID + "/" + g.Slug
	}
	return urls
}

// The gRPC guide's upstream TLS example verifies the backend's name, not
// only its CA: SNI alone selects a certificate and checks nothing.
func TestGRPCGuideVerifiesBackendName(t *testing.T) {
	for _, want := range []string{"trusted_ca:", "match_typed_subject_alt_names:", "san_type: DNS", "exact: orders.bb.internal"} {
		if !strings.Contains(gwGRPCClusterCode, want) {
			t.Errorf("gRPC guide's cluster example lacks %q", want)
		}
	}
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	body := s.get(t, "/docs/api-gateways/grpc", cookie).Body.String()
	if !strings.Contains(body, "match_typed_subject_alt_names") {
		t.Error("gRPC guide does not render the SAN matcher")
	}
}

// The gRPC guide's deadline example uses max_stream_duration, the setting
// that ends a call with DEADLINE_EXCEEDED, and contrasts the route timeout's
// 504 and UNAVAILABLE.
func TestGRPCGuideDeadlineMatchesStatus(t *testing.T) {
	for _, want := range []string{"max_stream_duration:", "grpc_timeout_header_max: 0.5s"} {
		if !strings.Contains(gwGRPCRoutesCode, want) {
			t.Errorf("gRPC guide's routes example lacks %q", want)
		}
	}
	if regexp.MustCompile(`(^|[ {])timeout: `).MatchString(gwGRPCRoutesCode) {
		t.Error("gRPC guide's routes example sets a route timeout, which ends a call with UNAVAILABLE, not DEADLINE_EXCEEDED")
	}
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	body := s.get(t, "/docs/api-gateways/grpc", cookie).Body.String()
	for _, want := range []string{"DEADLINE_EXCEEDED (downstream duration timeout)", "HTTP 504", "UNAVAILABLE (upstream request timeout)"} {
		if !strings.Contains(body, want) {
			t.Errorf("gRPC guide lacks %q", want)
		}
	}
}

// The resilience guide describes a retry budget as a concurrency limit with
// a floor, not a fixed multiple of the load.
func TestResilienceGuideBudgetIsConcurrency(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	body := s.get(t, "/docs/api-gateways/resilience", cookie).Body.String()
	for _, want := range []string{"active plus pending", "min_retry_concurrency", "One failing request on an otherwise idle cluster still gets all three of its retries", "specific to its traffic"} {
		if !strings.Contains(body, want) {
			t.Errorf("resilience guide lacks %q", want)
		}
	}
	for _, bad := range []string{"1.2 times normal", "near 1.2"} {
		if strings.Contains(body, bad) {
			t.Errorf("resilience guide still promises %q", bad)
		}
	}
}
