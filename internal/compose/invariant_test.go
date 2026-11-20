package compose

import (
	"bytes"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

// TestComposeInvariantDeterminism guarantees that rendering the same spec and
// state twice produces byte-for-byte identical files, paths, and modes.
func TestComposeInvariantDeterminism(t *testing.T) {
	s := &spec.Spec{
		Version: 1,
		Name:    "det-lab",
		Services: append(allServices(),
			spec.Service{
				Name:      "edge",
				Type:      "haproxy",
				Upstreams: []spec.Upstream{{Service: "api", Port: 8443}},
				Host:      &spec.Host{},
			},
		),
		Apps: []spec.App{
			{
				Name:     "api",
				Path:     "./api",
				Command:  []string{"api"},
				Replicas: 2,
				Uses:     uses("postgres", "redis", "kafka", "consul", "s3"),
				Host:     &spec.Host{Port: 8443},
			},
		},
	}

	b1 := render(t, s, secrets.Dotenv)
	b2 := render(t, s, secrets.Dotenv)

	if len(b1.Files) != len(b2.Files) {
		t.Fatalf("file count differed: %d vs %d", len(b1.Files), len(b2.Files))
	}

	for i := range b1.Files {
		f1, f2 := b1.Files[i], b2.Files[i]
		if f1.Path != f2.Path || f1.Mode != f2.Mode || !bytes.Equal(f1.Data, f2.Data) {
			t.Fatalf("file[%d] non-deterministic: (%s, %o) vs (%s, %o)", i, f1.Path, f1.Mode, f2.Path, f2.Mode)
		}
	}
}

// TestComposeInvariantDotenvSecretNonLeak guarantees that in dotenv mode,
// secret values live exclusively in .env and never leak into docker-compose.yml
// or any rendered service configuration file.
func TestComposeInvariantDotenvSecretNonLeak(t *testing.T) {
	s := &spec.Spec{
		Version:  1,
		Name:     "secret-lab",
		Services: allServices(),
		Apps: []spec.App{
			{
				Name:    "worker",
				Path:    "./worker",
				Command: []string{"worker"},
				Uses:    uses("postgres", "redis", "kafka", "consul", "s3"),
			},
		},
	}

	m, err := model.Resolve(s, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}

	store := &state.Secrets{}
	if _, err := secrets.Ensure(m, store); err != nil {
		t.Fatal(err)
	}

	var secretValues []string

	for _, svc := range m.Services {
		if svc.Kind != model.Infra {
			continue
		}

		for _, sec := range svc.Entry.Secrets {
			if v, ok := store.Get(svc.Name, sec.Key); ok && v != "" {
				secretValues = append(secretValues, v)
			}
		}
	}

	if len(secretValues) == 0 {
		t.Fatal("expected generated secrets for infra services")
	}

	certs := map[string]Cert{}
	for _, svc := range m.Services {
		certs[svc.Name] = Cert{Cert: []byte("CERT " + svc.Name + "\n"), Key: []byte("KEY " + svc.Name + "\n")}
	}

	b, err := Render(Input{
		Model:        m,
		ExperimentID: "exp-secret-invariant",
		Secrets:      secrets.NewMaterializer(secrets.Dotenv, store),
		CA:           []byte("CA-PEM\n"),
		Certs:        certs,
		AppDir:       func(app string) string { return "../../" + app },
	})
	if err != nil {
		t.Fatal(err)
	}

	envFile, ok := b.File(EnvFile)
	if !ok {
		t.Fatal("dotenv mode missing .env")
	}

	for _, val := range secretValues {
		if !strings.Contains(string(envFile.Data), val) {
			t.Errorf("secret missing from .env")
		}
	}

	for _, f := range b.Files {
		if f.Path == EnvFile {
			continue
		}

		for _, val := range secretValues {
			if strings.Contains(string(f.Data), val) {
				t.Errorf("dotenv secret leaked into %s", f.Path)
			}
		}
	}
}
