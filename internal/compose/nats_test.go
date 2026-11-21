package compose

import (
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

func TestComposeNatsService(t *testing.T) {
	s := &spec.Spec{
		Version: 1,
		Name:    "nats-lab",
		Services: []spec.Service{
			{
				Name:   "nats",
				Type:   "nats",
				Config: map[string]string{"jetstream": "true"},
			},
		},
		Apps: []spec.App{
			{Name: "worker", Path: "./worker", Command: []string{"worker"}, Uses: uses("nats")},
		},
	}

	b := render(t, s, secrets.Dotenv)

	var composeYML, natsConf string

	for _, f := range b.Files {
		switch f.Path {
		case "docker-compose.yml":
			composeYML = string(f.Data)
		case "config/nats/nats.conf":
			natsConf = string(f.Data)
		}
	}

	if !strings.Contains(composeYML, "nats:2.15.3-alpine3.22") {
		t.Error("compose missing pinned nats image")
	}

	if !strings.Contains(composeYML, "NATS_URL: tls://nats.bb.internal:4222") {
		t.Error("app missing NATS_URL")
	}

	if !strings.Contains(composeYML, "NATS_TOKEN: ${NATS__TOKEN}") {
		t.Errorf("compose missing dotenv NATS_TOKEN reference:\n%s", composeYML)
	}

	if !strings.Contains(natsConf, "http: 127.0.0.1:8222") || !strings.Contains(natsConf, "jetstream {") {
		t.Errorf("nats.conf missing loopback http or jetstream:\n%s", natsConf)
	}
}
