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
	if !strings.Contains(string(compose), "docker compose up -d --wait") {
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

func TestServiceDetailsIsReadOnlyAndHidesSecrets(t *testing.T) {
	h := setupStarter(t)
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "postgres"}); err != nil {
		t.Fatal(err)
	}
	// Before the first up: no certificates, and nothing gets generated.
	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "postgres"})
	if err != nil {
		t.Fatal(err)
	}

	if d.Certificate != nil {
		t.Error("certificate reported before any was issued")
	}

	if _, err := os.Stat(filepath.Join(h.dir, ".bb", "certs", "postgres")); !os.IsNotExist(err) {
		t.Error("ServiceDetails issued a certificate")
	}

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	d, err = h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "postgres"})
	if err != nil {
		t.Fatal(err)
	}

	if d.Kind != "infra" || d.Type != "postgres" || d.Address != "postgres.bb.internal:5432" || len(d.Listeners) != 1 {
		t.Fatalf("details = %+v", d)
	}

	secrets := 0

	for _, v := range d.Env[0].Vars {
		if v.Secret {
			secrets++

			if v.Value != "" {
				t.Errorf("%s leaked a secret value", v.Name)
			}
		}
	}

	if secrets == 0 {
		t.Error("no variable was marked secret")
	}

	if c := d.Certificate; c == nil || c.Issuer == "" || c.DNSNames[0] != "postgres.bb.internal" || !strings.HasPrefix(c.Identity, "spiffe://") {
		t.Errorf("certificate = %+v", d.Certificate)
	}

	app, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "consumer"})
	if err != nil {
		t.Fatal(err)
	}

	if app.Kind != "app" || app.Path != "./producer-consumer" || len(app.Env) != 2 || app.Env[1].Service != "kafka" {
		t.Fatalf("app details = %+v", app)
	}

	if app.Env[0].Vars[0].Name != "SSL_CERT_FILE" || app.Env[1].Vars[0].Name != "KAFKA_BROKERS" {
		t.Errorf("app env = %+v", app.Env)
	}

	k, _ := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "kafka"})
	if !slices.Equal(k.UsedBy, []string{"producer", "consumer"}) {
		t.Errorf("kafka used by = %v", k.UsedBy)
	}

	if _, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown service error = %v", err)
	}
}
