package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/errs"
	"github.com/brian-nunez/breadboard/internal/state"
)

func home(t *testing.T) state.Home {
	t.Helper()

	_ = os.Unsetenv("BB_DOMAIN")

	return state.Home{Dir: filepath.Join(t.TempDir(), "home")}
}

func TestDefaultsFileAndEnvLayers(t *testing.T) {
	ctx := context.Background()
	h := home(t)

	s, err := Load(ctx, h)
	if err != nil || s.Domain != DefaultDomain {
		t.Fatalf("defaults = %+v %v", s, err)
	}

	if _, err := h.Ensure(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(h.ConfigPath(), []byte("# my settings\nversion: 1\ndomain: file.internal # chosen on purpose\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if s, _ := Load(ctx, h); s.Domain != "file.internal" {
		t.Fatalf("file layer = %+v", s)
	}

	t.Setenv("BB_DOMAIN", "env.internal")

	if s, _ := Load(ctx, h); s.Domain != "env.internal" {
		t.Fatalf("env layer = %+v", s)
	}

	t.Setenv("BB_DOMAIN", "NOT VALID")

	if _, err := Load(ctx, h); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("invalid env domain error = %v", err)
	}
}

func TestSetKeepsComments(t *testing.T) {
	ctx := context.Background()

	h := home(t)
	if _, err := h.Ensure(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(h.ConfigPath(), []byte("# my settings\nversion: 1\ndomain: a.internal # chosen on purpose\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	v, err := Set(ctx, h, "domain", "b.internal")
	if err != nil || v.Value != "b.internal" || v.Source != "config.yaml" {
		t.Fatalf("Set = %+v %v", v, err)
	}

	data, _ := os.ReadFile(h.ConfigPath())
	for _, want := range []string{"# my settings", "# chosen on purpose", "domain: b.internal"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config.yaml lacks %q:\n%s", want, data)
		}
	}
}

func TestNewerConfigIsRefused(t *testing.T) {
	ctx := context.Background()

	h := home(t)
	if _, err := h.Ensure(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(h.ConfigPath(), []byte("version: 5\ndomain: x.internal\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(ctx, h); !errors.Is(err, errs.ErrNewerFormat) {
		t.Fatalf("Load error = %v", err)
	}

	if _, err := Set(ctx, h, "domain", "y.internal"); !errors.Is(err, errs.ErrNewerFormat) {
		t.Fatalf("Set error = %v", err)
	}
}

func TestDomainMustBePrivate(t *testing.T) {
	ctx := context.Background()

	h := home(t)
	for _, d := range []string{"example.com", "bb.internal.com", "local"} {
		if _, err := Set(ctx, h, "domain", d); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("Set(domain, %q) error = %v, want ErrValidation", d, err)
		}
	}

	for _, d := range []string{"bb.internal", "lab.test", "x.localhost", "lab.home.arpa"} {
		if _, err := Set(ctx, h, "domain", d); err != nil {
			t.Errorf("Set(domain, %q) = %v", d, err)
		}
	}
}
