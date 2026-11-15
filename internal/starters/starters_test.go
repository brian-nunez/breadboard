package starters

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryStarterRenders(t *testing.T) {
	for _, s := range All() {
		files, err := Render(s, "lab")
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
		for _, want := range []string{"AS dev", "AS prod", "USER ", "golang:" + GoImage, "LABEL maintainer="} {
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
			files, err := Render(s, "lab")
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
