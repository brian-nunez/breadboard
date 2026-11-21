package catalog

import (
	"strings"
	"testing"
)

func TestNatsEntry(t *testing.T) {
	e, ok := Lookup("nats")
	if !ok {
		t.Fatal("nats not registered in catalog")
	}

	if e.SupportsPlaintext() {
		t.Error("nats must not expose a plaintext listener")
	}

	if len(e.Listeners) != 1 || e.Listeners[0].Protocol != "nats" || e.Listeners[0].Port != NatsClientPort {
		t.Fatalf("unexpected listeners: %+v", e.Listeners)
	}

	if err := e.CheckConfig("jetstream", "true"); err != nil {
		t.Errorf("jetstream=true rejected: %v", err)
	}

	if err := e.CheckConfig("jetstream", "false"); err != nil {
		t.Errorf("jetstream=false rejected: %v", err)
	}

	if err := e.CheckConfig("jetstream", "maybe"); err == nil {
		t.Error("jetstream=maybe should fail validation")
	}

	// JetStream enabled (default).
	c := e.Server(Server{
		Name:   "bus",
		FQDN:   "bus.bb.internal",
		Domain: "bb.internal",
		Config: e.ResolveConfig(nil),
		Secret: func(k string) string { return "SECRET_" + k },
	})

	if len(c.Files) != 1 || c.Files[0].Name != "nats.conf" {
		t.Fatalf("unexpected files: %+v", c.Files)
	}

	conf := string(c.Files[0].Data)
	for _, want := range []string{
		`server_name: "bus"`,
		"listen: 0.0.0.0:4222",
		"http: 127.0.0.1:8222",
		`cert_file: "/etc/bb/tls/tls.crt"`,
		`key_file: "/etc/bb/tls/tls.key"`,
		`ca_file: "/etc/bb/ca.pem"`,
		"jetstream {",
		`store_dir: "/data"`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nats.conf missing %q:\n%s", want, conf)
		}
	}

	// Secret must never be written into nats.conf.
	if strings.Contains(conf, "SECRET_token") {
		t.Error("nats.conf leaked token secret")
	}

	// JetStream disabled.
	coreOnly := e.Server(Server{
		Name:   "bus",
		FQDN:   "bus.bb.internal",
		Domain: "bb.internal",
		Config: e.ResolveConfig(map[string]string{"jetstream": "false"}),
		Secret: func(k string) string { return "SECRET_" + k },
	})
	if strings.Contains(string(coreOnly.Files[0].Data), "jetstream {") {
		t.Errorf("jetstream=false should omit jetstream block:\n%s", coreOnly.Files[0].Data)
	}

	env := e.Client(Client{
		Service: "bus",
		FQDN:    "bus.bb.internal",
		Config:  e.ResolveConfig(nil),
		Secret:  func(k string) string { return "SECRET_" + k },
	})

	got := map[string]string{}
	for _, v := range env {
		got[v.Name] = v.Value
	}

	if got["NATS_URL"] != "tls://bus.bb.internal:4222" || got["NATS_TOKEN"] != "SECRET_token" || got["NATS_CA"] != CAFile {
		t.Errorf("unexpected client env: %+v", got)
	}
}
