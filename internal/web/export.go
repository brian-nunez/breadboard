package web

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/brian-nunez/breadboard/internal/core"
)

// staticKey marks a request rendered for the static site.
type staticKey struct{}

// isStatic reports whether ctx renders a page for the static site, which
// has no experiment: pages leave out what needs one.
func isStatic(ctx context.Context) bool {
	v, _ := ctx.Value(staticKey{}).(bool)

	return v
}

// Export writes the docs, the starters and the landing page to dir as a
// static site. Every page comes from the same handlers as bb serve.
func Export(ctx context.Context, eng *core.Engine, dir string) error {
	ctx = context.WithValue(ctx, staticKey{}, true)

	st, err := eng.Starters(ctx)
	if err != nil {
		return err
	}

	pages := [][2]string{
		{"/", "index.html"},
		{"/starters", "starters/index.html"},
		{"/docs", "docs/index.html"},
		{"/search-index", "search-index.json"},
	}
	for _, x := range st.Starters {
		pages = append(pages, [2]string{"/starters/" + x.ID, "starters/" + x.ID + "/index.html"})
	}

	for _, t := range techs {
		pages = append(pages, [2]string{"/docs/" + t.ID, "docs/" + t.ID + "/index.html"})
		for _, g := range t.Guides {
			pages = append(pages, [2]string{"/docs/" + t.ID + "/" + g.Slug, "docs/" + t.ID + "/" + g.Slug + "/index.html"})
		}
	}

	s := &Server{eng: eng}
	mux := s.routes()

	for _, p := range pages {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, p[0], nil))

		if rec.Code != http.StatusOK {
			return fmt.Errorf("GET %s: status %d", p[0], rec.Code)
		}

		if err := writeFile(dir, p[1], rec.Body.Bytes()); err != nil {
			return err
		}
	}

	var notFound bytes.Buffer
	if err := notFoundPage().Render(ctx, &notFound); err != nil {
		return err
	}

	if err := writeFile(dir, "404.html", notFound.Bytes()); err != nil {
		return err
	}

	return fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}

		return writeFile(dir, path, data)
	})
}

// writeFile writes data to name under dir, creating its directories.
func writeFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}
