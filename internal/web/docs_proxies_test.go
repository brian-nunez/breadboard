package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/starters"
)

// The load-balancing and proxy docs sections (D-157).
var proxyDocsSections = []string{"load-balancing", "haproxy", "envoy", "traefik", "nginx", "pgbouncer"}

// Every page of the proxy docs sections is in the search index.
func TestProxyDocsAreInSearch(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	rec := s.get(t, "/search-index", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("/search-index = %d", rec.Code)
	}

	var entries []searchEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}

	urls := map[string]bool{}
	for _, e := range entries {
		urls[e.URL] = true
	}

	for _, id := range proxyDocsSections {
		tc, ok := findTech(id)
		if !ok {
			t.Errorf("docs section %s is not registered", id)

			continue
		}

		if !urls["/docs/"+id] {
			t.Errorf("search has no /docs/%s", id)
		}

		for _, g := range tc.Guides {
			if !urls["/docs/"+id+"/"+g.Slug] {
				t.Errorf("search has no /docs/%s/%s", id, g.Slug)
			}
		}
	}
}

// Every link from one docs page to another lands on a page that exists.
func TestDocsInternalLinksResolve(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	href := regexp.MustCompile(`href="(/docs/[^"#]*)`)

	var pages []string
	for _, tc := range techs {
		pages = append(pages, "/docs/"+tc.ID)
		for _, g := range tc.Guides {
			pages = append(pages, "/docs/"+tc.ID+"/"+g.Slug)
		}
	}

	for _, page := range pages {
		for _, m := range href.FindAllStringSubmatch(s.get(t, page, cookie).Body.String(), -1) {
			if !slices.Contains(pages, m[1]) && m[1] != "/docs" {
				t.Errorf("%s links %s, which is not a docs page", page, m[1])
			}
		}
	}
}

// Every starter a docs page names is registered, and every /starters/ link
// on a rendered docs page lands on a registered starter page.
func TestDocsStarterLinksAreKnown(t *testing.T) {
	known := map[string]bool{}
	for _, st := range starters.All() {
		known[st.ID] = true
	}

	check := func(where, id string) {
		if id != "" && !known[id] {
			t.Errorf("%s links starter %q, which does not exist", where, id)
		}
	}
	for _, tc := range techs {
		for _, id := range tc.Starters {
			check("/docs/"+tc.ID, id)
		}

		for _, g := range tc.Guides {
			check("/docs/"+tc.ID+"/"+g.Slug, g.Starter)
		}
	}

	for _, l := range []link{stRoundRobin, stLeastConn, stP2C, stHashing, stSticky, stCanary, stBlueGreen, stHealth, stL4L7, stTLSModes, stMirroring, stDraining, stPoolModes, stPoolSidecar, stPathHost, stTransform, stVersioning, stRateLimit, stBreaking, stRetries, stJWT, stFwdAuth, stBFF, stGRPC, stSidecarCB} {
		check("docStarters", l.URL)

		if strings.Count(l.URL, "/") != 1 {
			t.Errorf("starter id %q is not <area>/<name>", l.URL)
		}
	}

	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	href := regexp.MustCompile(`href="/starters/([^"#?]+)`)
	served := map[string]bool{}

	for _, tc := range techs {
		pages := []string{"/docs/" + tc.ID}
		for _, g := range tc.Guides {
			pages = append(pages, "/docs/"+tc.ID+"/"+g.Slug)
		}

		for _, page := range pages {
			for _, m := range href.FindAllStringSubmatch(s.get(t, page, cookie).Body.String(), -1) {
				check(page, m[1])

				if _, seen := served[m[1]]; !seen {
					served[m[1]] = s.get(t, "/starters/"+m[1], cookie).Code == http.StatusOK
				}

				if !served[m[1]] {
					t.Errorf("%s links /starters/%s, which does not answer 200", page, m[1])
				}
			}
		}
	}
}

// Every registered starter has a narrative whose Guide is a registered docs
// page, so the starter page's "Read the guide" link never dead-ends.
func TestStarterGuidesResolve(t *testing.T) {
	pages := map[string]bool{}
	for _, tc := range techs {
		pages["/docs/"+tc.ID] = true
		for _, g := range tc.Guides {
			pages["/docs/"+tc.ID+"/"+g.Slug] = true
		}
	}

	for _, st := range starters.All() {
		c, ok := starterContents[st.ID]
		if !ok {
			t.Errorf("starter %s has no narrative", st.ID)

			continue
		}

		if !pages[c.Guide] {
			t.Errorf("starter %s guide %q is not a docs page", st.ID, c.Guide)
		}
	}
}
