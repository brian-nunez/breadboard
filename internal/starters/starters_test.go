package starters

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/catalog"
)

func TestEveryStarterRenders(t *testing.T) {
	for _, s := range All() {
		files, err := Render(s, s.DefaultLab("lab", "bb.internal"))
		if err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
		have := map[string]string{}
		for _, f := range files {
			have[f.Path] = string(f.Data)
		}
		for _, want := range []string{"Dockerfile", "README.md", "go.mod", "go.sum"} {
			if _, ok := have[want]; !ok {
				t.Errorf("%s has no %s", s.ID, want)
			}
		}
		if !strings.Contains(have["go.mod"], "go "+GoVersion) {
			t.Errorf("%s go.mod does not target Go %s", s.ID, GoVersion)
		}
		df := have["Dockerfile"]
		for _, want := range []string{"AS dev", "AS prod", "USER ", "golang:" + GoImage, "LABEL maintainer=", "go build -o /usr/local/bin/ ./cmd/..."} {
			if !strings.Contains(df, want) {
				t.Errorf("%s Dockerfile lacks %q", s.ID, want)
			}
		}
		if strings.Contains(df, ":latest") {
			t.Errorf("%s Dockerfile uses a latest tag", s.ID)
		}
		readme := have["README.md"]
		for _, want := range []string{"## Try this", "## Break this"} {
			if !strings.Contains(readme, want) {
				t.Errorf("%s README lacks %q", s.ID, want)
			}
		}
		for path, data := range have {
			if strings.Contains(data, `"github.com/brian-nunez/`) {
				t.Errorf("%s: %s imports a Breadboard or bkit package; starters use official libraries only", s.ID, path)
			}
		}
	}
}

// TestRenderedStartersBuild renders each starter and runs go build and go vet
// on it, as a user would. It downloads modules, so -short skips it.
func TestRenderedStartersBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads modules")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	for _, s := range All() {
		t.Run(s.ID, func(t *testing.T) {
			dir := t.TempDir()
			files, err := Render(s, s.DefaultLab("lab", "bb.internal"))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				p := filepath.Join(dir, filepath.FromSlash(f.Path))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, f.Data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=go"+GoVersion)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
		})
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("kafka/producer-consumer"); !ok {
		t.Fatal("kafka/producer-consumer missing")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("Lookup found a missing starter")
	}
}

// App names are unique across starters, so applying several starters to one
// experiment never clashes with itself.
func TestStarterAppNamesAreUnique(t *testing.T) {
	owner := map[string]string{}
	for _, s := range All() {
		for _, a := range s.Apps {
			if prev, ok := owner[a.Name]; ok {
				t.Errorf("app %q is in both %s and %s", a.Name, prev, s.ID)
			}
			owner[a.Name] = s.ID
		}
	}
}

// Service names are unique across starters and never collide with an app
// name of any starter or a catalog type, and every upstream names an app of its own starter
// or a type it requires.
func TestStarterServiceNamesAreUnique(t *testing.T) {
	owner := map[string]string{}
	for _, s := range All() {
		for _, a := range s.Apps {
			owner[a.Name] = s.ID
		}
	}
	for _, s := range All() {
		for _, svc := range s.Services {
			if prev, ok := owner[svc.Name]; ok {
				t.Errorf("service %q is in both %s and %s", svc.Name, prev, s.ID)
			}
			owner[svc.Name] = s.ID
			// A required type is added under its own name.
			if _, ok := catalog.Lookup(svc.Name); ok {
				t.Errorf("%s service %q is named after a catalog type", s.ID, svc.Name)
			}
			for _, u := range svc.Upstreams {
				if !slices.ContainsFunc(s.Apps, func(a App) bool { return a.Name == u.Service }) && !slices.Contains(s.Requires, u.Service) {
					t.Errorf("%s service %s upstream %q is neither an app of the starter nor a type it requires", s.ID, svc.Name, u.Service)
				}
			}
		}
	}
}

// Each app runs the binary the dev stage built from cmd/<app>, so the program
// is the container's main process.
func TestStarterAppsRunTheirBuiltBinary(t *testing.T) {
	for _, s := range All() {
		files, err := Render(s, s.DefaultLab("lab", "bb.internal"))
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, f := range files {
			have[f.Path] = true
		}
		for _, a := range s.Apps {
			if len(a.Command) != 1 || a.Command[0] != a.Name {
				t.Errorf("%s app %s command = %v, want [%s]", s.ID, a.Name, a.Command, a.Name)
			}
			if !have["cmd/"+a.Name+"/main.go"] {
				t.Errorf("%s app %s has no cmd/%s/main.go", s.ID, a.Name, a.Name)
			}
		}
	}
}
