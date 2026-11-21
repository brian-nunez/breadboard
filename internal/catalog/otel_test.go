package catalog

import (
	"strings"
	"testing"
)

func TestOtelConfigValidation(t *testing.T) {
	e, ok := Lookup("otel")
	if !ok {
		t.Fatal("otel entry missing")
	}

	for _, good := range []string{"logs", "metrics", "traces", "logs,traces", "metrics,traces", "logs,metrics,traces"} {
		if err := e.CheckConfig("signals", good); err != nil {
			t.Errorf("signals=%q: unexpected error: %v", good, err)
		}
	}

	for _, bad := range []string{"", "profiles", "logs, logs", "traces,traces", "logs,", ",metrics"} {
		if err := e.CheckConfig("signals", bad); err == nil {
			t.Errorf("signals=%q: expected error, got nil", bad)
		}
	}
}

func TestMergeOtelSignals(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"traces", "metrics", "metrics,traces"},
		{"logs", "traces", "logs,traces"},
		{"logs,traces", "metrics,traces", "logs,metrics,traces"},
		{"traces", "traces", "traces"},
	}

	for _, tc := range cases {
		if got := MergeOtelSignals(tc.a, tc.b); got != tc.want {
			t.Errorf("MergeOtelSignals(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestOtelServerSelectiveSignalsAndLoopback(t *testing.T) {
	e, _ := Lookup("otel")

	render := func(signals string) (Container, map[string]string) {
		c := e.Server(Server{
			Name:   "otel",
			FQDN:   "otel.bb.internal",
			Domain: "bb.internal",
			Config: e.ResolveConfig(map[string]string{"signals": signals}),
			Secret: func(string) string { return "secretpw" },
		})

		files := map[string]string{}
		for _, f := range c.Files {
			files[f.Name] = string(f.Data)
		}

		return c, files
	}

	t.Run("traces only", func(t *testing.T) {
		c, files := render("traces")
		if _, ok := files["loki-config.yaml"]; ok {
			t.Error("loki-config.yaml should not be rendered when logs disabled")
		}

		if _, ok := files["tempo-config.yaml"]; !ok {
			t.Error("tempo-config.yaml missing when traces enabled")
		}

		if strings.Contains(files["tempo-config.yaml"], "remote_write") {
			t.Error("tempo metrics_generator remote_write should be omitted when metrics disabled")
		}

		if !strings.Contains(files["tempo-config.yaml"], "http_listen_address: 127.0.0.1") {
			t.Error("tempo must bind to 127.0.0.1")
		}

		col := files["otelcol-config.yaml"]
		if !strings.Contains(col, "cert_file: "+CertFile) || !strings.Contains(col, "key_file: "+KeyFile) {
			t.Error("collector must enable TLS on OTLP receivers")
		}

		if !strings.Contains(col, "traces:") || strings.Contains(col, "metrics:\n      receivers:") || strings.Contains(col, "logs:\n      receivers:") {
			t.Errorf("unexpected collector pipelines for traces-only:\n%s", col)
		}

		start := files["start.sh"]
		if !strings.Contains(start, "start_bg tempo") || strings.Contains(start, "start_bg loki") || strings.Contains(start, "start_bg prometheus") {
			t.Errorf("unexpected start.sh for traces-only:\n%s", start)
		}

		if strings.Contains(strings.Join(c.Health.Test, " "), "3100") || strings.Contains(strings.Join(c.Health.Test, " "), "9090") {
			t.Errorf("health check should not probe disabled backends: %v", c.Health.Test)
		}
	})

	t.Run("metrics only", func(t *testing.T) {
		c, _ := render("metrics")
		health := strings.Join(c.Health.Test, " ")

		if !strings.Contains(health, ":4318/v1/metrics") || !strings.Contains(health, ":9090/-/ready") {
			t.Errorf("metrics-only health check = %s", health)
		}
	})

	t.Run("logs only", func(t *testing.T) {
		c, _ := render("logs")
		health := strings.Join(c.Health.Test, " ")

		if !strings.Contains(health, ":4318/v1/logs") || !strings.Contains(health, ":3100/ready") {
			t.Errorf("logs-only health check = %s", health)
		}
	})

	t.Run("all signals", func(t *testing.T) {
		c, files := render("logs,metrics,traces")
		for _, want := range []string{"otelcol-config.yaml", "grafana-datasources.yaml", "start.sh", "loki-config.yaml", "tempo-config.yaml"} {
			if _, ok := files[want]; !ok {
				t.Errorf("missing generated file %s", want)
			}
		}

		if !strings.Contains(files["loki-config.yaml"], "http_listen_address: 127.0.0.1") {
			t.Error("loki must bind to 127.0.0.1")
		}

		if !strings.Contains(files["start.sh"], "--web.listen-address=127.0.0.1:9090") {
			t.Error("prometheus must bind to 127.0.0.1:9090")
		}

		if !strings.Contains(files["tempo-config.yaml"], "remote_write") {
			t.Error("tempo metrics_generator should wire to prometheus when metrics enabled")
		}

		health := strings.Join(c.Health.Test, " ")
		for _, port := range []string{":3000/api/health", ":13133/ready", ":4318/v1/traces", ":3100/ready", ":9090/-/ready", ":3200/ready"} {
			if !strings.Contains(health, port) {
				t.Errorf("health check missing %s: %s", port, health)
			}
		}
	})
}

func TestOtelClientContract(t *testing.T) {
	e, _ := Lookup("otel")
	vars := e.Client(Client{
		Service: "otel",
		FQDN:    "otel.bb.internal",
		Config:  e.ResolveConfig(map[string]string{"signals": "logs,traces"}),
		Secret:  func(string) string { return "pw" },
	})

	got := map[string]string{}
	for _, v := range vars {
		got[v.Name] = v.Value
	}

	if got["OTEL_EXPORTER_OTLP_ENDPOINT"] != "https://otel.bb.internal:4317" {
		t.Errorf("endpoint = %q", got["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}

	if got["OTEL_TRACES_EXPORTER"] != "otlp" || got["OTEL_LOGS_EXPORTER"] != "otlp" || got["OTEL_METRICS_EXPORTER"] != "none" {
		t.Errorf("exporter flags = traces:%q metrics:%q logs:%q", got["OTEL_TRACES_EXPORTER"], got["OTEL_METRICS_EXPORTER"], got["OTEL_LOGS_EXPORTER"])
	}

	if got["GRAFANA_URL"] != "https://otel.bb.internal:3000" {
		t.Errorf("grafana url = %q", got["GRAFANA_URL"])
	}
}
