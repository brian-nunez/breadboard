package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/brian-nunez/breadboard/"

// wrappers may talk to the user; everything else is the core or below it.
var wrappers = map[string]bool{"cli": true, "web": true, "mcp": true}

// forbidden lists user I/O and process control the core must never use.
var forbidden = map[string]map[string]bool{
	"fmt": {"Print": true, "Printf": true, "Println": true},
	"os":  {"Exit": true, "Stdin": true, "Stdout": true, "Stderr": true},
	"log": {"Print": true, "Printf": true, "Println": true, "Fatal": true, "Fatalf": true, "Fatalln": true, "Panic": true, "Panicf": true, "Panicln": true},
}

// allowedInternalImports locks Breadboard's package dependency DAG. Every
// production package under bb/ and internal/ must appear here with the exact
// set of internal packages it is permitted to import. Wrappers (cli, web, mcp)
// must go through internal/core; leaf packages stay leaf packages.
var allowedInternalImports = map[string][]string{
	"bb":                       {"internal/cli", "internal/core"},
	"internal/cli":             {"internal/core", "internal/mcp", "internal/web"},
	"internal/web":             {"internal/core", "internal/spec"},
	"internal/web/site":        {"internal/core", "internal/web"},
	"internal/mcp":             {"internal/core"},
	"internal/core":            {"internal/catalog", "internal/catalog/sidecar", "internal/compose", "internal/config", "internal/errs", "internal/model", "internal/pki", "internal/runtime", "internal/secrets", "internal/spec", "internal/starters", "internal/state"},
	"internal/compose":         {"internal/catalog", "internal/catalog/sidecar", "internal/model", "internal/secrets"},
	"internal/config":          {"internal/errs", "internal/spec", "internal/state"},
	"internal/model":           {"internal/catalog", "internal/catalog/sidecar", "internal/errs", "internal/pki", "internal/spec"},
	"internal/secrets":         {"internal/catalog", "internal/model", "internal/state"},
	"internal/starters":        {"internal/catalog", "internal/spec", "starters"},
	"internal/catalog":         {"internal/spec"},
	"internal/catalog/sidecar": {"internal/catalog", "internal/spec"},
	"internal/spec":            {"internal/errs"},
	"internal/runtime":         {"internal/errs"},
	"internal/state":           {"internal/errs"},
	"internal/pki":             {},
	"internal/errs":            {},
	"internal/designdocs":      {},
}

// allowedOSExec lists the only non-test files permitted to import os/exec.
var allowedOSExec = map[string]bool{
	"internal/runtime/runtime.go": true,
	"internal/cli/serve.go":       true,
}

// TestCoreHasNoUserIO enforces the wrapper boundary: internal/core and every
// package below it never print, read stdin, exit, or parse flags.
func TestCoreHasNoUserIO(t *testing.T) {
	root := ".."

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0

	for _, e := range entries {
		if !e.IsDir() || wrappers[e.Name()] {
			continue
		}

		err := filepath.WalkDir(filepath.Join(root, e.Name()), func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir // test fixtures (such as test apps) are not part of the core
			}

			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}

			checked++

			checkFile(t, path)

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if checked < 10 {
		t.Fatalf("only %d files checked; the walk is broken", checked)
	}
}

// TestPackageArchitectureBoundaries enforces the full internal dependency DAG
// and stdlib restrictions (os/exec, unsafe, math/rand in security packages)
// across all production Go files in bb/ and internal/.
func TestPackageArchitectureBoundaries(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	seenPkgs := map[string]bool{}
	checked := 0

	for _, top := range []string{"bb", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}

			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}

			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}

			rel = filepath.ToSlash(rel)
			pkg := filepath.ToSlash(filepath.Dir(rel))
			seenPkgs[pkg] = true
			checked++

			allowedList, ok := allowedInternalImports[pkg]
			if !ok {
				t.Errorf("%s: package %q is not registered in allowedInternalImports; declare its architectural layer explicitly", rel, pkg)

				return nil
			}

			allowed := make(map[string]bool, len(allowedList))
			for _, a := range allowedList {
				allowed[a] = true
			}

			checkArchFile(t, path, rel, pkg, allowed)

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if checked < 30 {
		t.Fatalf("only %d files checked; the walk is broken", checked)
	}

	for pkg := range allowedInternalImports {
		if !seenPkgs[pkg] && pkg != "internal/designdocs" {
			t.Errorf("allowedInternalImports contains unknown or empty package %q", pkg)
		}
	}
}

func checkArchFile(t *testing.T, path, rel, pkg string, allowed map[string]bool) {
	t.Helper()

	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}

	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		pos := fset.Position(imp.Pos())

		if p == "unsafe" {
			t.Errorf("%s: imports unsafe, which is forbidden", pos)
		}

		if p == "os/exec" && !allowedOSExec[rel] {
			t.Errorf("%s: imports os/exec; subprocess execution is restricted to %v", pos, allowedOSExec)
		}

		if (pkg == "internal/pki" || pkg == "internal/secrets") && (p == "math/rand" || p == "math/rand/v2") {
			t.Errorf("%s: imports %s in %s; use crypto/rand", pos, p, pkg)
		}

		if rest, ok := strings.CutPrefix(p, modulePrefix); ok {
			if !allowed[rest] {
				t.Errorf("%s: package %s must not import %s (allowed: %v)", pos, pkg, rest, allowedInternalImports[pkg])
			}
		}
	}
}

// TestNoNolintDirectives ensures no Go source file silently bypasses linters
// with //nolint directives. Legitimate exceptions belong in .golangci.yml with
// a documented rule, where guardrail review catches changes.
func TestNoNolintDirectives(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	checked := 0

	for _, top := range []string{"bb", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}

			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_templ.go") {
				return err
			}

			checked++
			fset := token.NewFileSet()

			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}

			for _, cg := range f.Comments {
				for _, c := range cg.List {
					trimmed := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
					if strings.HasPrefix(trimmed, "nolint") {
						t.Errorf("%s: //nolint directive is forbidden (%s); fix the issue or document a narrow rule in .golangci.yml", fset.Position(c.Pos()), c.Text)
					}
				}
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if checked < 40 {
		t.Fatalf("only %d files checked; the walk is broken", checked)
	}
}

func checkFile(t *testing.T, path string) {
	t.Helper()

	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]string{} // local name -> import path

	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p == "flag" {
			t.Errorf("%s imports flag; flag parsing belongs in a wrapper", fset.Position(imp.Pos()))
		}

		local := filepath.Base(p)
		if imp.Name != nil {
			local = imp.Name.Name
		}

		names[local] = p
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}

		if bad := forbidden[names[id.Name]]; bad[sel.Sel.Name] {
			t.Errorf("%s uses %s.%s; the core must not do user I/O", fset.Position(sel.Pos()), names[id.Name], sel.Sel.Name)
		}

		return true
	})
}
