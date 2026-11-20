package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCoreContractCatalogAndEnv locks the exact catalog service types,
// pinned Docker images, resource limits, notes, and app env contracts.
func TestCoreContractCatalogAndEnv(t *testing.T) {
	h := newHarness(t)

	cat, err := h.eng.Catalog(h.ctx())
	if err != nil {
		t.Fatal(err)
	}

	wantTypes := []string{
		"consul", "envoy", "haproxy", "kafka", "nginx",
		"pgbouncer", "postgres", "redis", "s3-compatible", "traefik",
	}

	gotTypes := make([]string, len(cat.Entries))
	for i, e := range cat.Entries {
		gotTypes[i] = e.Type
		if e.Image == "" || strings.HasSuffix(e.Image, ":latest") || !strings.Contains(e.Image, ":") {
			t.Errorf("catalog %s has non-pinned image %q", e.Type, e.Image)
		}

		if e.Memory == "" || e.CPUs == "" {
			t.Errorf("catalog %s missing resource limits (memory=%q cpus=%q)", e.Type, e.Memory, e.CPUs)
		}

		if len(e.Ports) == 0 {
			t.Errorf("catalog %s declares no ports", e.Type)
		}

		learn, err := h.eng.Learn(h.ctx(), LearnRequest{Type: e.Type})
		if err != nil || strings.TrimSpace(learn.Notes) == "" {
			t.Errorf("catalog %s missing learning notes: %v", e.Type, err)
		}
	}

	slices.Sort(gotTypes)

	if !slices.Equal(gotTypes, wantTypes) {
		t.Fatalf("catalog types drifted:\n got: %v\nwant: %v", gotTypes, wantTypes)
	}
}

// TestCoreContractErrorSentinelsAndExport locks core sentinel errors and the
// ExportCompose contract in inline and dotenv modes.
func TestCoreContractErrorSentinelsAndExport(t *testing.T) {
	h := newHarness(t)

	// Uninitialized directory returns ErrNotInitialized.
	if _, err := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Creds on empty dir: got %v, want ErrNotInitialized", err)
	}

	if _, err := h.eng.Init(h.ctx(), InitRequest{Dir: h.dir, Name: "core-contract"}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "postgres"}); err != nil {
		t.Fatal(err)
	}

	// Duplicate service returns ErrExists.
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "postgres"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate AddService: got %v, want ErrExists", err)
	}

	// Unknown catalog type returns ErrNotFound; invalid service name returns ErrValidation.
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "not-a-service"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown type AddService: got %v, want ErrNotFound", err)
	}

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "postgres", Name: "BAD_NAME"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid name AddService: got %v, want ErrValidation", err)
	}

	// AddApp contract: generates runnable scaffold without leaking secrets.
	appRes, err := h.eng.AddApp(h.ctx(), AddAppRequest{Dir: h.dir, Name: "api"})
	if err != nil {
		t.Fatal(err)
	}

	creds, err := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir, Service: "postgres"})
	if err != nil || len(creds.Services) != 1 {
		t.Fatalf("Creds: %+v, err=%v", creds, err)
	}

	var pgPassword string

	for _, kv := range creds.Services[0].Env {
		if kv.Name == "PGPASSWORD" {
			pgPassword = kv.Value
		}
	}

	if pgPassword == "" {
		t.Fatal("expected non-empty PGPASSWORD in creds")
	}

	// AddAppResult must redact secret values and never write them into generated files.
	for _, use := range appRes.Uses {
		for _, v := range use.Env {
			if v.Secret && v.Value != "" {
				t.Errorf("AddAppResult leaked secret value for %s", v.Name)
			}
		}
	}

	mainGo, err := os.ReadFile(filepath.Join(h.dir, "api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(mainGo), pgPassword) {
		t.Fatal("AddApp leaked secret password into generated main.go")
	}

	// ExportCompose inline vs dotenv contract.
	inlineDir := filepath.Join(t.TempDir(), "inline")
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: inlineDir, Secrets: "inline"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(inlineDir, ".env")); err == nil {
		t.Error("inline export should not create .env")
	}

	dotenvDir := filepath.Join(t.TempDir(), "dotenv")
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: dotenvDir, Secrets: "dotenv"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dotenvDir, ".env")); err != nil {
		t.Errorf("dotenv export missing .env: %v", err)
	}
}
