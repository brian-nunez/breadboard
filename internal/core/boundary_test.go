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

// wrappers may talk to the user; everything else is the core or below it.
var wrappers = map[string]bool{"cli": true, "web": true, "mcp": true}

// forbidden lists user I/O and process control the core must never use.
var forbidden = map[string]map[string]bool{
	"fmt": {"Print": true, "Printf": true, "Println": true},
	"os":  {"Exit": true, "Stdin": true, "Stdout": true, "Stderr": true},
	"log": {"Print": true, "Printf": true, "Println": true, "Fatal": true, "Fatalf": true, "Fatalln": true, "Panic": true, "Panicf": true, "Panicln": true},
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
