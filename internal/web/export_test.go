package web

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
)

// linkAttr matches an attribute that names a page or file of the site.
var linkAttr = regexp.MustCompile(`\s(?:href|src|data-load|data-live)="([^"]*)"`)

func TestExportWritesAStaticSite(t *testing.T) {
	eng, err := core.New(core.Options{Home: filepath.Join(t.TempDir(), "home"), Runtime: nullRuntime{}, Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := Export(context.Background(), eng, dir); err != nil {
		t.Fatal(err)
	}

	want := []string{"index.html", "404.html", "docs/index.html", "starters/index.html", "search-index.json", "static/app.css", "docs/kafka/consumer-groups/index.html", "starters/kafka/producer-consumer/index.html"}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".html" {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		body := string(data)
		name, _ := filepath.Rel(dir, path)

		for _, bad := range []string{`href="/dashboard"`, `href="/architecture"`, `href="/services/`, `data-load="/fragments`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %s", name, bad)
			}
		}

		for _, m := range linkAttr.FindAllStringSubmatch(body, -1) {
			link := m[1]
			if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") {
				continue
			}

			link, _, _ = strings.Cut(link, "#")
			link, _, _ = strings.Cut(link, "?")

			target := filepath.Join(dir, filepath.FromSlash(link), "index.html")
			if strings.HasPrefix(link, "/static/") || strings.HasSuffix(link, ".json") {
				target = filepath.Join(dir, filepath.FromSlash(link))
			}

			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s links %s, which is not exported", name, m[1])
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	index, err := os.ReadFile(filepath.Join(dir, "search-index.json"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(index), "/services/") {
		t.Errorf("search index lists services: %s", index)
	}
}

// Static mode is set only by Export: bb serve still offers the dashboard.
func TestServedLandingPageSkipsToTheDashboard(t *testing.T) {
	s := newProject(t)

	body := s.get(t, "/", map[string]string{"Cookie": signIn(t, s)}).Body.String()
	if !strings.Contains(body, `href="/dashboard"`) || !strings.Contains(body, "Skip to dashboard →") {
		t.Error("served landing page lacks the Skip to dashboard link")
	}
}
