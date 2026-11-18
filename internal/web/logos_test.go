package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/catalog/sidecar"
)

// Every catalog and sidecar type has a logo served from /static/logos/, and
// no logo can run script or load anything.
func TestEveryTechHasASafeLogo(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	var types []string
	for _, e := range catalog.All() {
		types = append(types, e.ID)
	}

	for _, typ := range sidecar.All() {
		types = append(types, typ.ID)
	}

	unsafe := regexp.MustCompile(`(?i)<script|\son\w+\s*=|href`)
	// The Breadboard mark stands in for docs topics without a logo.
	types = append(types, "breadboard")
	for _, typ := range types {
		src := logoSrc(typ)
		if typ == "breadboard" {
			src = "/static/logos/breadboard.svg"
		}

		if src == "" {
			t.Errorf("%s has no logo", typ)

			continue
		}

		rec := s.get(t, src, cookie)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %s = %d", typ, src, rec.Code)

			continue
		}

		if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("%s: content type %q", src, ct)
		}

		if m := unsafe.FindString(rec.Body.String()); m != "" {
			t.Errorf("%s contains %q", src, m)
		}
	}

	if logoSrc("my-app") != "" {
		t.Error("an unknown type has a logo")
	}
}

// A docs topic without a tech logo (Getting started) shows the Breadboard
// mark in the docs home, its page title and the sidebar, and its
// breadcrumb sits above the title row, not beside the badge.
func TestTopicsWithoutALogoShowTheBreadboardMark(t *testing.T) {
	s := newServer(t)

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for _, path := range []string{"/docs", "/docs/getting-started", "/docs/kafka"} {
		if body := s.get(t, path, cookie).Body.String(); !strings.Contains(body, "/static/logos/breadboard.svg") {
			t.Errorf("%s does not show the Breadboard mark", path)
		}
	}

	body := s.get(t, "/docs/getting-started", cookie).Body.String()
	nav, h1 := strings.Index(body, `<nav class="text-sm text-slate-500"><a href="/docs"`), strings.Index(body, "<h1")

	badge := strings.LastIndex(body[:max(h1, 0)], "/static/logos/breadboard.svg")
	if nav < 0 || h1 < 0 || badge < 0 || nav > badge {
		t.Errorf("the breadcrumb should come before the title badge (nav %d, badge %d, h1 %d)", nav, badge, h1)
	}
}

// Pages that show a catalog tech show its logo; an app's badge keeps its
// letter.
func TestPagesShowTechLogos(t *testing.T) {
	s := newProject(t)

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for _, path := range []string{"/", "/docs", "/dashboard"} {
		body := s.get(t, path, cookie).Body.String()
		if !strings.Contains(body, `<img src="/static/logos/kafka.svg"`) {
			t.Errorf("%s lacks the Kafka logo", path)
		}
	}

	body := s.get(t, "/dashboard", cookie).Body.String()

	links := regexp.MustCompile(`(?s)<a href="/services/sink".*?</a>`).FindAllString(body, -1)
	if len(links) == 0 {
		t.Fatal("the overview does not list the sink app")
	}

	for _, l := range links {
		if strings.Contains(l, "/static/logos/") || !strings.Contains(l, "rounded-full") {
			t.Errorf("app badge is not a lettered round badge: %s", l)
		}
	}
}
