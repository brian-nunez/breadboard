package spec

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// FuzzParseAndValidate ensures arbitrary YAML bytes never panic Parse, Decode,
// or Validate, and that any valid document round-trips cleanly through Bytes.
func FuzzParseAndValidate(f *testing.F) {
	f.Add([]byte("version: 1\nname: lab\n"))
	f.Add([]byte("version: 1\nname: lab\nservices:\n  db:\n    type: postgres\napps:\n  api:\n    path: ./api\n    uses: [db]\n"))
	f.Add([]byte("version: 1\nname: lab\nservices:\n  edge:\n    type: traefik\n    host: true\n"))
	f.Add([]byte(":\n  - [\n"))
	f.Add([]byte(""))
	f.Add([]byte("null"))
	f.Add([]byte("version: 999\nname: BAD\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Parse(data)
		if err != nil {
			return
		}

		s, err := doc.Decode()
		if err != nil {
			return
		}

		if err := Validate(s, types); err != nil {
			return
		}

		out, err := doc.Bytes()
		if err != nil {
			t.Fatalf("valid document failed Bytes(): %v", err)
		}

		doc2, err := Parse(out)
		if err != nil {
			t.Fatalf("round-trip Bytes() failed Parse(): %v\n%s", err, out)
		}

		s2, err := doc2.Decode()
		if err != nil {
			t.Fatalf("round-trip Bytes() failed Decode(): %v", err)
		}

		if s2.Name != s.Name || len(s2.Services) != len(s.Services) || len(s2.Apps) != len(s.Apps) {
			t.Fatalf("round-trip lost structure: before=%+v after=%+v", s, s2)
		}
	})
}

// TestSpecInvariantRoundTrip generates deterministic randomized valid specs
// through Document mutations and verifies Bytes -> Parse -> Decode -> Validate
// preserves every service, app, use, replica count, and host flag.
func TestSpecInvariantRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	svcTypes := []string{"postgres", "redis", "kafka", "consul"}

	for iter := range 40 {
		doc := New("lab")

		numServices := 1 + rng.IntN(len(svcTypes))
		perm := rng.Perm(len(svcTypes))[:numServices]

		var addedServices []string

		for _, idx := range perm {
			typ := svcTypes[idx]
			if err := doc.AddService(typ, typ); err != nil {
				t.Fatalf("iter %d AddService(%s): %v", iter, typ, err)
			}

			addedServices = append(addedServices, typ)
		}

		numApps := 1 + rng.IntN(3)
		for a := range numApps {
			name := "app" + string(rune('a'+a))
			useCount := rng.IntN(len(addedServices) + 1)

			var appUses []Use

			for _, svcName := range addedServices[:useCount] {
				appUses = append(appUses, Use{Service: svcName})
			}

			app := App{
				Name:     name,
				Path:     "./" + name,
				Command:  []string{name},
				Uses:     appUses,
				Replicas: 1 + rng.IntN(3),
			}
			if rng.IntN(2) == 1 {
				app.Host = &Host{Port: 8443}
			}

			if err := doc.AddApp(app); err != nil {
				t.Fatalf("iter %d AddApp(%s): %v", iter, name, err)
			}
		}

		raw, err := doc.Bytes()
		if err != nil {
			t.Fatalf("iter %d Bytes: %v", iter, err)
		}

		doc2, err := Parse(raw)
		if err != nil {
			t.Fatalf("iter %d Parse: %v\n%s", iter, err, raw)
		}

		s1, err := doc.Decode()
		if err != nil {
			t.Fatalf("iter %d Decode1: %v", iter, err)
		}

		s2, err := doc2.Decode()
		if err != nil {
			t.Fatalf("iter %d Decode2: %v", iter, err)
		}

		if err := Validate(s2, types); err != nil {
			t.Fatalf("iter %d Validate: %v\n%s", iter, err, raw)
		}

		if s1.Name != s2.Name || len(s1.Services) != len(s2.Services) || len(s1.Apps) != len(s2.Apps) {
			t.Fatalf("iter %d round-trip mismatch", iter)
		}

		for i := range s1.Services {
			if s1.Services[i].Name != s2.Services[i].Name || s1.Services[i].Type != s2.Services[i].Type {
				t.Errorf("iter %d service[%d] drifted: %+v vs %+v", iter, i, s1.Services[i], s2.Services[i])
			}
		}

		for i := range s1.Apps {
			a1, a2 := s1.Apps[i], s2.Apps[i]
			if a1.Name != a2.Name || a1.Path != a2.Path || a1.Replicas != a2.Replicas || !slices.Equal(a1.Command, a2.Command) || len(a1.Uses) != len(a2.Uses) {
				t.Errorf("iter %d app[%d] drifted: %+v vs %+v", iter, i, a1, a2)
			}

			if (a1.Host == nil) != (a2.Host == nil) || (a1.Host != nil && a1.Host.Port != a2.Host.Port) {
				t.Errorf("iter %d app[%d].Host drifted: %+v vs %+v", iter, i, a1.Host, a2.Host)
			}
		}
	}
}
