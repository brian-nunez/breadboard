package catalog

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// FuzzTraefikMergeStatic ensures arbitrary user static_file YAML never panics
// traefikMergeStatic, never allows overriding Breadboard-owned top-level keys
// (regardless of case, tags, anchors, or aliases), and always produces valid
// YAML when accepted.
func FuzzTraefikMergeStatic(f *testing.F) {
	f.Add([]byte("metrics:\n  prometheus:\n    entryPoint: traefik\n"))
	f.Add([]byte("entryPoints:\n  web:\n    address: ':80'\n"))
	f.Add([]byte("EntryPoints:\n  web:\n    address: ':80'\n"))
	f.Add([]byte("providers:\n  docker: {}\n"))
	f.Add([]byte("api:\n  insecure: true\n"))
	f.Add([]byte("ping: {}\n"))
	f.Add([]byte("global:\n  checkNewVersion: true\n"))
	f.Add([]byte("!!binary ZW50cnlQb2ludHM=: {}\n"))
	f.Add([]byte("<<: {entryPoints: {}}\n"))
	f.Add([]byte("log:\n  level: DEBUG\n"))
	f.Add([]byte("{}"))
	f.Add([]byte(""))
	f.Add([]byte("- not-a-mapping"))

	f.Fuzz(func(t *testing.T, data []byte) {
		s := Server{
			Name:   "edge",
			FQDN:   "edge.bb.internal",
			Domain: "bb.internal",
			UserStatic: &UserFile{
				Path: "./traefik-static.yml",
				Data: data,
			},
		}

		out, err := traefikMergeStatic(s)
		if err != nil {
			return
		}

		var doc yaml.Node
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatalf("accepted static_file produced invalid YAML: %v\n%s", err, out)
		}

		if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
			t.Fatalf("merged traefik.yml is not a single mapping")
		}

		// No Breadboard-owned top-level key may appear more than once (case-insensitive).
		seen := map[string]int{}

		m := doc.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			seen[strings.ToLower(m.Content[i].Value)]++
		}

		for _, owned := range traefikOwnedStatic {
			if seen[strings.ToLower(owned.key)] > 1 {
				t.Fatalf("owned key %q duplicated after merge:\n%s", owned.key, out)
			}
		}
	})
}
