package catalog

import (
	"strings"
	"testing"
)

func TestParseOpenSearchIndices(t *testing.T) {
	got, err := ParseOpenSearchIndices("")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}

	got, err = ParseOpenSearchIndices("vehicles,events,vehicles")
	if err != nil {
		t.Fatalf("valid indices: %v", err)
	}

	if len(got) != 2 || got[0] != "vehicles" || got[1] != "events" {
		t.Fatalf("deduped indices = %v", got)
	}

	for _, bad := range []string{"vehicles, events", "Vehicles", "_internal", "-bad", "..", "bad/name"} {
		if _, err := ParseOpenSearchIndices(bad); err == nil {
			t.Errorf("ParseOpenSearchIndices(%q): expected error", bad)
		}
	}
}

func TestOpenSearchEntry(t *testing.T) {
	e, ok := Lookup("opensearch")
	if !ok {
		t.Fatal("opensearch not registered in catalog")
	}

	if e.SupportsPlaintext() {
		t.Error("opensearch must not expose a plaintext listener")
	}

	if len(e.Listeners) != 1 || e.Listeners[0].Protocol != "https" || e.Listeners[0].Port != OpenSearchHTTPPort {
		t.Fatalf("unexpected listeners: %+v", e.Listeners)
	}

	c := e.Server(Server{
		Name:   "search",
		FQDN:   "search.bb.internal",
		Domain: "bb.internal",
		Config: e.ResolveConfig(map[string]string{"indices": "vehicles,products"}),
		Secret: func(k string) string { return "SECRET_" + k },
	})

	var confYML, startSH string

	for _, f := range c.Files {
		switch f.Name {
		case "opensearch.yml":
			confYML = string(f.Data)
		case "start.sh":
			startSH = string(f.Data)
		}
	}

	for _, want := range []string{
		"cluster.name: search",
		"discovery.type: single-node",
		"http.publish_host: search.bb.internal",
		"transport.host: 127.0.0.1",
		"plugins.security.disabled: false",
		"plugins.security.ssl.http.enabled: true",
		`"CN=search.bb.internal,O=Breadboard"`,
	} {
		if !strings.Contains(confYML, want) {
			t.Errorf("opensearch.yml missing %q:\n%s", want, confYML)
		}
	}

	for _, want := range []string{
		"hash.sh",
		"internal_users.yml",
		"BB_INDICES",
		"opensearch-docker-entrypoint.sh",
	} {
		if !strings.Contains(startSH, want) {
			t.Errorf("start.sh missing %q", want)
		}
	}

	healthCmd := strings.Join(c.Health.Test, " ")
	if !strings.Contains(healthCmd, "--cacert /etc/bb/ca.pem") || !strings.Contains(healthCmd, "https://search.bb.internal:9200/_cluster/health") {
		t.Errorf("unexpected health check: %s", healthCmd)
	}

	env := e.Client(Client{
		Service: "search",
		FQDN:    "search.bb.internal",
		Config:  e.ResolveConfig(nil),
		Secret:  func(k string) string { return "SECRET_" + k },
	})

	got := map[string]string{}
	for _, v := range env {
		got[v.Name] = v.Value
	}

	if got["OPENSEARCH_URL"] != "https://search.bb.internal:9200" {
		t.Errorf("OPENSEARCH_URL = %q", got["OPENSEARCH_URL"])
	}

	if got["OPENSEARCH_USER"] != "admin" || got["OPENSEARCH_PASSWORD"] != "SECRET_password" || got["OPENSEARCH_CACERT"] != CAFile {
		t.Errorf("unexpected client env: %+v", got)
	}
}
