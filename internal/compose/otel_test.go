package compose

import (
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

func TestComposeOtelServiceAndHostAccess(t *testing.T) {
	s := &spec.Spec{
		Version: 1,
		Name:    "otel-lab",
		Services: []spec.Service{
			{
				Name:   "otel",
				Type:   "otel",
				Config: map[string]string{"signals": "traces,logs"},
				Host:   &spec.Host{},
			},
		},
		Apps: []spec.App{
			{Name: "api", Path: "./api", Command: []string{"api"}, Uses: uses("otel")},
		},
	}

	b := render(t, s, secrets.Dotenv)

	var composeYML, otelCol, hostDyn string

	for _, f := range b.Files {
		switch f.Path {
		case "docker-compose.yml":
			composeYML = string(f.Data)
		case "config/otel/otelcol-config.yaml":
			otelCol = string(f.Data)
		case "config/bb-host/dynamic.yml":
			hostDyn = string(f.Data)
		}
	}

	if !strings.Contains(composeYML, "grafana/otel-lgtm:0.41.1") {
		t.Errorf("compose missing pinned otel-lgtm image")
	}

	if !strings.Contains(composeYML, "OTEL_EXPORTER_OTLP_ENDPOINT: https://otel.bb.internal:4317") {
		t.Errorf("app missing OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	if !strings.Contains(composeYML, "OTEL_METRICS_EXPORTER: none") || !strings.Contains(composeYML, "OTEL_TRACES_EXPORTER: otlp") {
		t.Errorf("app missing per-signal exporter env vars")
	}

	if strings.Contains(otelCol, "otlp_http/metrics") {
		t.Errorf("collector should not include metrics exporter when metrics disabled")
	}

	if !strings.Contains(hostDyn, "otel.otel-lab.localhost") || !strings.Contains(hostDyn, "https://otel.bb.internal:3000") {
		t.Errorf("bb-host should route otel.otel-lab.localhost to Grafana :3000:\n%s", hostDyn)
	}
}
