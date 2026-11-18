package secrets

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

func TestGenerate(t *testing.T) {
	safe := regexp.MustCompile(`^[A-Z2-7]{32}$`)
	seen := map[string]bool{}

	for range 50 {
		pw, err := Generate(catalog.Password)
		if err != nil || !safe.MatchString(pw) || seen[pw] {
			t.Fatalf("password %q (%v) is not a fresh 32-character URL-safe value", pw, err)
		}

		seen[pw] = true
	}

	id, err := Generate(catalog.ClusterID)
	if err != nil {
		t.Fatal(err)
	}

	if b, err := base64.RawURLEncoding.DecodeString(id); err != nil || len(b) != 16 {
		t.Fatalf("cluster id %q is not 16 bytes of base64url", id)
	}
}

func TestEnsureAndMaterialize(t *testing.T) {
	m, err := model.Resolve(&spec.Spec{Name: "lab", Services: []spec.Service{{Name: "db", Type: "postgres"}, {Name: "kafka", Type: "kafka"}}}, "bb.internal")
	if err != nil {
		t.Fatal(err)
	}

	store := &state.Secrets{}
	store.Set("gone", "password", "old")

	added, err := Ensure(m, store)
	if err != nil || !added {
		t.Fatalf("Ensure = %v %v", added, err)
	}

	pw, _ := store.Get("db", "password")
	if again, _ := Ensure(m, store); again {
		t.Error("second Ensure generated new secrets")
	}

	if v, _ := store.Get("db", "password"); v != pw {
		t.Error("Ensure replaced an existing secret")
	}

	if v, _ := store.Get("gone", "password"); v != "old" {
		t.Error("Ensure dropped the secret of a removed service; its data volume still expects it")
	}

	inline := NewMaterializer(Inline, store)
	if got := inline.Ref("db")("password"); got != pw {
		t.Errorf("inline ref = %q", got)
	}

	if inline.Dotenv() != nil {
		t.Error("inline produced a .env")
	}

	dot := NewMaterializer(Dotenv, store)
	if got := dot.Ref("db")("superuser-password"); got != "${DB__SUPERUSER_PASSWORD}" {
		t.Errorf("dotenv ref = %q", got)
	}

	env := string(dot.Dotenv())
	if !strings.Contains(env, "DB__SUPERUSER_PASSWORD=") || strings.Contains(env, "DB__PASSWORD=") {
		t.Errorf(".env should hold exactly the referenced secrets:\n%s", env)
	}
}

func TestVarNamesDoNotCollide(t *testing.T) {
	if VarName("x", "superuser-password") == VarName("x-superuser", "password") {
		t.Fatal("VarName collides across services")
	}
}

func TestClusterIDNeverStartsWithDash(t *testing.T) {
	for range 2000 {
		id, err := Generate(catalog.ClusterID)
		if err != nil || id[0] == '-' {
			t.Fatalf("cluster id %q (%v)", id, err)
		}
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": Inline, "inline": Inline, "dotenv": Dotenv} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q %v", in, got, err)
		}
	}

	if _, err := ParseMode("vault"); err == nil {
		t.Error("ParseMode accepted vault")
	}
}
