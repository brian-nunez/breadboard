package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCreds(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("postgres")
	h.add("redis")
	res, err := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir, Service: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 1 || len(res.Services[0].Secrets) != 2 {
		t.Fatalf("creds = %+v", res)
	}
	pw := res.Services[0].Secrets[1].Value
	var url string
	for _, v := range res.Services[0].Env {
		if v.Name == "DATABASE_URL" {
			url = v.Value
		}
	}
	if url != "postgres://app:"+pw+"@postgres.bb.internal:5432/app?sslmode=verify-full" {
		t.Errorf("DATABASE_URL = %q", url)
	}
	again, _ := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir})
	if len(again.Services) != 2 || again.Services[0].Secrets[1].Value != pw {
		t.Error("secrets changed between calls")
	}
	if _, err := h.eng.Creds(h.ctx(), CredsRequest{Dir: h.dir, Service: "kafka"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown service error = %v", err)
	}
}

func TestExportCompose(t *testing.T) {
	h := setupStarter(t)
	out := filepath.Join(t.TempDir(), "stack")
	res, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out, Secrets: "dotenv"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Files, ".env") || !slices.Contains(res.Files, "README.md") {
		t.Fatalf("files = %v", res.Files)
	}
	compose, _ := os.ReadFile(filepath.Join(out, "docker-compose.yml"))
	if !strings.Contains(string(compose), "docker compose -f docker-compose.yml") {
		t.Error("export header lacks the run command")
	}
	rel, _ := filepath.Rel(out, filepath.Join(h.dir, "producer-consumer"))
	if !strings.Contains(string(compose), "context: "+filepath.ToSlash(rel)) {
		t.Errorf("app context is not relative to the export:\n%s", compose)
	}
	ignore, _ := os.ReadFile(filepath.Join(out, ".gitignore"))
	if !strings.Contains(string(ignore), ".env") {
		t.Error(".gitignore lacks .env")
	}
	if h.rt.calls != nil && slices.Contains(h.rt.calls, "up") {
		t.Error("export started containers")
	}

	// Re-export replaces bb's own directory; a foreign one is refused.
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: out}); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".env")); !os.IsNotExist(err) {
		t.Error("inline re-export left the old .env behind")
	}
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: foreign}); !errors.Is(err, ErrExists) {
		t.Fatalf("foreign dir error = %v", err)
	}
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Out: h.dir}); !errors.Is(err, ErrValidation) {
		t.Fatalf("project dir error = %v", err)
	}
	if _, err := h.eng.ExportCompose(h.ctx(), ExportComposeRequest{Dir: h.dir, Secrets: "vault"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad secrets mode error = %v", err)
	}
}
