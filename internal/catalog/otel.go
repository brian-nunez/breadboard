package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	otelImage        = "grafana/otel-lgtm:0.41.1"
	otelGRPCPort     = 4317
	otelHTTPPort     = 4318
	otelGrafanaPort  = 3000
	otelDefaultSigs  = "logs,metrics,traces"
	otelGrafanaUser  = "admin"
	otelDatasourcesP = "/otel-lgtm/grafana/conf/provisioning/datasources/grafana-datasources.yaml"
)

var validOtelSignals = []string{"logs", "metrics", "traces"}

// OtelSignals reports which telemetry signals are enabled by an otel service's
// signals config option. An empty value defaults to all three signals.
func OtelSignals(raw string) (logs, metrics, traces bool) {
	if strings.TrimSpace(raw) == "" {
		raw = otelDefaultSigs
	}

	for part := range strings.SplitSeq(raw, ",") {
		switch strings.TrimSpace(part) {
		case "logs":
			logs = true
		case "metrics":
			metrics = true
		case "traces":
			traces = true
		}
	}

	return logs, metrics, traces
}

// MergeOtelSignals returns the canonical comma-separated union of two signal
// lists, in logs,metrics,traces order.
func MergeOtelSignals(a, b string) string {
	la, ma, ta := OtelSignals(a)
	lb, mb, tb := OtelSignals(b)

	var out []string
	if la || lb {
		out = append(out, "logs")
	}

	if ma || mb {
		out = append(out, "metrics")
	}

	if ta || tb {
		out = append(out, "traces")
	}

	if len(out) == 0 {
		return otelDefaultSigs
	}

	return strings.Join(out, ",")
}

func validateOtelSignals(v string) error {
	if v == "" {
		return errors.New("must enable at least one signal: logs, metrics, traces")
	}

	if strings.ContainsAny(v, " \t\r\n") {
		return errors.New("separate signals with commas, no whitespace")
	}

	seen := map[string]bool{}

	for part := range strings.SplitSeq(v, ",") {
		if !slices.Contains(validOtelSignals, part) {
			return fmt.Errorf("unknown signal %q (known: logs, metrics, traces)", part)
		}

		if seen[part] {
			return fmt.Errorf("signal %q is listed more than once", part)
		}

		seen[part] = true
	}

	return nil
}

func otel() *Entry {
	return &Entry{
		ID:      "otel",
		Title:   "OpenTelemetry (LGTM)",
		Summary: "OpenTelemetry Collector with Loki, Prometheus, Tempo and Grafana: OTLP TLS on 4317/4318, Grafana HTTPS on 3000.",
		Image:   otelImage,
		// otlp-grpc is Listeners[0] so `uses: [otel]` creates an OTLP gRPC TLS edge.
		// grafana is the first https listener so `host: true` routes the host URL to Grafana.
		Listeners: []Listener{
			{Name: "otlp-grpc", Protocol: "grpc", Port: otelGRPCPort},
			{Name: "grafana", Protocol: "https", Port: otelGrafanaPort},
			{Name: "otlp-http", Protocol: "otlp-http", Port: otelHTTPPort},
		},
		Resources: Resources{Memory: "1g", CPUs: "1.0"},
		Secrets: []Secret{
			{Key: "grafana-password", Kind: Password, Help: "Grafana admin password (user: admin)"},
		},
		Config: []ConfigOption{
			{
				Key:      "signals",
				Default:  otelDefaultSigs,
				Help:     "comma-separated signals to enable: logs, metrics, traces",
				Validate: validateOtelSignals,
			},
		},
		DataPath: "/data",
		Notes:    notes("otel"),
		Server:   otelServer,
		Client:   otelClient,
	}
}

func otelServer(s Server) Container {
	logs, metrics, traces := OtelSignals(s.Config["signals"])

	files := []File{
		{Name: "otelcol-config.yaml", Data: []byte(otelCollectorConfig(logs, metrics, traces))},
		{Name: "grafana-datasources.yaml", Mount: otelDatasourcesP, Data: []byte(otelGrafanaDatasources(logs, metrics, traces))},
		{Name: "start.sh", Mode: 0o755, Data: []byte(otelStartScript(logs, metrics, traces))},
	}

	if logs {
		files = append(files, File{Name: "loki-config.yaml", Data: []byte(otelLokiConfig())})
	}

	if traces {
		files = append(files, File{Name: "tempo-config.yaml", Data: []byte(otelTempoConfig(metrics))})
	}

	// Verify Grafana HTTPS and OTLP HTTP against the lab CA, plus the loopback
	// readiness endpoints for the collector and every enabled backend.
	otlpPath := "/v1/traces"

	if !traces {
		if metrics {
			otlpPath = "/v1/metrics"
		} else {
			otlpPath = "/v1/logs"
		}
	}

	checks := []string{
		fmt.Sprintf("curl -sf --cacert %s https://%s:%d/api/health >/dev/null", CAFile, s.FQDN, otelGrafanaPort),
		"curl -sf http://127.0.0.1:13133/ready >/dev/null",
		fmt.Sprintf(`curl -s --cacert %s -o /dev/null -w '%%{http_code}' https://%s:%d%s | grep -Eq '^(200|400|405|415)$'`, CAFile, s.FQDN, otelHTTPPort, otlpPath),
	}
	if logs {
		checks = append(checks, "curl -sf http://127.0.0.1:3100/ready >/dev/null")
	}

	if metrics {
		checks = append(checks, "curl -sf http://127.0.0.1:9090/-/ready >/dev/null")
	}

	if traces {
		checks = append(checks, "curl -sf http://127.0.0.1:3200/ready >/dev/null")
	}

	return Container{
		Entrypoint: []string{"/bin/bash", ConfigDir + "/start.sh"},
		Environment: []EnvVar{
			{Name: "BB_GRAFANA_PASSWORD", Value: s.Secret("grafana-password"), Secret: true},
		},
		Files: files,
		Health: Health{
			Test:          []string{"CMD-SHELL", strings.Join(checks, " && ")},
			Interval:      10 * time.Second,
			Timeout:       5 * time.Second,
			Retries:       6,
			StartPeriod:   45 * time.Second,
			StartInterval: 1 * time.Second,
		},
	}
}

func otelClient(c Client) []EnvVar {
	logs, metrics, traces := OtelSignals(c.Config["signals"])
	exp := func(on bool) string {
		if on {
			return "otlp"
		}

		return "none"
	}

	return []EnvVar{
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: fmt.Sprintf("https://%s:%d", c.FQDN, otelGRPCPort)},
		{Name: "OTEL_EXPORTER_OTLP_HTTP_ENDPOINT", Value: fmt.Sprintf("https://%s:%d", c.FQDN, otelHTTPPort)},
		{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "grpc"},
		{Name: "OTEL_EXPORTER_OTLP_CERTIFICATE", Value: CAFile},
		{Name: "OTEL_TRACES_EXPORTER", Value: exp(traces)},
		{Name: "OTEL_METRICS_EXPORTER", Value: exp(metrics)},
		{Name: "OTEL_LOGS_EXPORTER", Value: exp(logs)},
		{Name: "GRAFANA_URL", Value: fmt.Sprintf("https://%s:%d", c.FQDN, otelGrafanaPort)},
		{Name: "GRAFANA_USER", Value: otelGrafanaUser},
		{Name: "GRAFANA_PASSWORD", Value: c.Secret("grafana-password"), Secret: true},
	}
}

func otelCollectorConfig(logs, metrics, traces bool) string {
	var b strings.Builder

	b.WriteString("# Generated by Breadboard: OTLP over TLS; internal backends on loopback.\n")
	b.WriteString("receivers:\n")
	b.WriteString("  otlp:\n")
	b.WriteString("    protocols:\n")
	b.WriteString("      grpc:\n")
	fmt.Fprintf(&b, "        endpoint: 0.0.0.0:%d\n", otelGRPCPort)
	b.WriteString("        tls:\n")
	fmt.Fprintf(&b, "          cert_file: %s\n", CertFile)
	fmt.Fprintf(&b, "          key_file: %s\n", KeyFile)
	b.WriteString("      http:\n")
	fmt.Fprintf(&b, "        endpoint: 0.0.0.0:%d\n", otelHTTPPort)
	b.WriteString("        tls:\n")
	fmt.Fprintf(&b, "          cert_file: %s\n", CertFile)
	fmt.Fprintf(&b, "          key_file: %s\n", KeyFile)
	b.WriteString("\nextensions:\n")
	b.WriteString("  health_check:\n")
	b.WriteString("    endpoint: 127.0.0.1:13133\n")
	b.WriteString("    path: /ready\n")
	b.WriteString("\nprocessors:\n")
	b.WriteString("  batch:\n")
	b.WriteString("    timeout: 1s\n")
	b.WriteString("\nexporters:\n")

	if traces {
		b.WriteString("  otlp_http/traces:\n")
		b.WriteString("    endpoint: http://127.0.0.1:4418\n")
		b.WriteString("    tls:\n")
		b.WriteString("      insecure: true\n")
	}

	if metrics {
		b.WriteString("  otlp_http/metrics:\n")
		b.WriteString("    endpoint: http://127.0.0.1:9090/api/v1/otlp\n")
		b.WriteString("    tls:\n")
		b.WriteString("      insecure: true\n")
	}

	if logs {
		b.WriteString("  otlp_http/logs:\n")
		b.WriteString("    endpoint: http://127.0.0.1:3100/otlp\n")
		b.WriteString("    tls:\n")
		b.WriteString("      insecure: true\n")
	}

	b.WriteString("\nservice:\n")
	b.WriteString("  extensions: [health_check]\n")
	b.WriteString("  telemetry:\n")
	b.WriteString("    metrics:\n")
	b.WriteString("      level: none\n")
	b.WriteString("  pipelines:\n")

	if traces {
		b.WriteString("    traces:\n")
		b.WriteString("      receivers: [otlp]\n")
		b.WriteString("      processors: [batch]\n")
		b.WriteString("      exporters: [otlp_http/traces]\n")
	}

	if metrics {
		b.WriteString("    metrics:\n")
		b.WriteString("      receivers: [otlp]\n")
		b.WriteString("      processors: [batch]\n")
		b.WriteString("      exporters: [otlp_http/metrics]\n")
	}

	if logs {
		b.WriteString("    logs:\n")
		b.WriteString("      receivers: [otlp]\n")
		b.WriteString("      processors: [batch]\n")
		b.WriteString("      exporters: [otlp_http/logs]\n")
	}

	return b.String()
}

func otelLokiConfig() string {
	return `# Generated by Breadboard: Loki bound to loopback only.
auth_enabled: false

server:
  http_listen_address: 127.0.0.1
  http_listen_port: 3100
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: 9095

common:
  instance_addr: 127.0.0.1
  path_prefix: /data/loki
  storage:
    filesystem:
      chunks_directory: /data/loki/chunks
      rules_directory: /data/loki/rules
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h

pattern_ingester:
  lifecycler:
    min_ready_duration: 1s

ingester:
  lifecycler:
    min_ready_duration: 1s

frontend:
  scheduler_dns_lookup_period: 1s
  address: 127.0.0.1

query_scheduler:
  use_scheduler_ring: false
`
}

func otelTempoConfig(withMetrics bool) string {
	var b strings.Builder

	b.WriteString(`# Generated by Breadboard: Tempo bound to loopback only.
server:
  http_listen_address: 127.0.0.1
  http_listen_port: 3200
  grpc_listen_address: 127.0.0.1
  grpc_listen_port: 9096

distributor:
  receivers:
    otlp:
      protocols:
        http:
          endpoint: "127.0.0.1:4418"

memberlist:
  bind_addr: [127.0.0.1]
  bind_port: 7947

querier:
  frontend_worker:
    frontend_address: 127.0.0.1:9096

storage:
  trace:
    backend: local
    wal:
      path: /data/tempo/wal
    local:
      path: /data/tempo/blocks
`)

	if withMetrics {
		b.WriteString(`
metrics_generator:
  processor:
    span_metrics:
      dimensions:
        - service_name
        - operation
        - status_code
  storage:
    path: /data/tempo/generator/wal
    remote_write:
      - url: http://127.0.0.1:9090/api/v1/write
        send_exemplars: true

overrides:
  defaults:
    metrics_generator:
      processors: [service-graphs, span-metrics]
`)
	}

	return b.String()
}

func otelGrafanaDatasources(logs, metrics, traces bool) string {
	var b strings.Builder

	b.WriteString("# Generated by Breadboard: datasources for the enabled OTel signals.\n")
	b.WriteString("apiVersion: 1\n\ndatasources:\n")

	if metrics {
		b.WriteString("  - name: Prometheus\n")
		b.WriteString("    type: prometheus\n")
		b.WriteString("    uid: prometheus\n")
		b.WriteString("    url: http://127.0.0.1:9090\n")
		b.WriteString("    editable: true\n")
		b.WriteString("    jsonData:\n")
		b.WriteString("      timeInterval: 15s\n")

		if traces {
			b.WriteString("      exemplarTraceIdDestinations:\n")
			b.WriteString("        - name: trace_id\n")
			b.WriteString("          datasourceUid: tempo\n")
			b.WriteString("          urlDisplayLabel: \"Trace: $${__value.raw}\"\n")
		}
	}

	if traces {
		b.WriteString("  - name: Tempo\n")
		b.WriteString("    type: tempo\n")
		b.WriteString("    uid: tempo\n")
		b.WriteString("    url: http://127.0.0.1:3200\n")
		b.WriteString("    editable: true\n")
		b.WriteString("    jsonData:\n")
		b.WriteString("      search:\n")
		b.WriteString("        hide: false\n")
		b.WriteString("      nodeGraph:\n")
		b.WriteString("        enabled: true\n")

		if logs {
			b.WriteString("      tracesToLogsV2:\n")
			b.WriteString("        customQuery: true\n")
			b.WriteString("        datasourceUid: loki\n")
			b.WriteString("        query: '{$${__tags}} | trace_id = \"$${__trace.traceId}\"'\n")
			b.WriteString("        tags:\n")
			b.WriteString("          - key: service.name\n")
			b.WriteString("            value: service_name\n")
			b.WriteString("      lokiSearch:\n")
			b.WriteString("        datasourceUid: loki\n")
		}

		if metrics {
			b.WriteString("      serviceMap:\n")
			b.WriteString("        datasourceUid: prometheus\n")
		}
	}

	if logs {
		b.WriteString("  - name: Loki\n")
		b.WriteString("    type: loki\n")
		b.WriteString("    uid: loki\n")
		b.WriteString("    url: http://127.0.0.1:3100\n")
		b.WriteString("    editable: true\n")

		if traces {
			b.WriteString("    jsonData:\n")
			b.WriteString("      derivedFields:\n")
			b.WriteString("        - name: trace_id\n")
			b.WriteString("          matcherType: label\n")
			b.WriteString("          matcherRegex: trace_id\n")
			b.WriteString("          url: \"$${__value.raw}\"\n")
			b.WriteString("          datasourceUid: tempo\n")
			b.WriteString("          urlDisplayLabel: \"Trace: $${__value.raw}\"\n")
		}
	}

	return b.String()
}

func otelStartScript(logs, metrics, traces bool) string {
	var b strings.Builder

	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by Breadboard: starts only the enabled LGTM backends, Grafana over HTTPS, and OTel Collector over TLS.\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("mkdir -p /data/grafana/data /data/grafana/plugins\n")

	if logs {
		b.WriteString("mkdir -p /data/loki\n")
	}

	if metrics {
		b.WriteString("mkdir -p /data/prometheus\n")
	}

	if traces {
		b.WriteString("mkdir -p /data/tempo/wal /data/tempo/blocks /data/tempo/generator/wal\n")
	}

	b.WriteString("\npids=()\nnames=()\n\n")
	b.WriteString("start_bg() {\n")
	b.WriteString("  local name=\"$1\"; shift\n")
	b.WriteString("  \"$@\" &\n")
	b.WriteString("  pids+=($!)\n")
	b.WriteString("  names+=(\"$name\")\n")
	b.WriteString("}\n\n")
	b.WriteString("stop_all() {\n")
	b.WriteString("  for pid in \"${pids[@]:-}\"; do\n")
	b.WriteString("    kill -TERM \"$pid\" 2>/dev/null || true\n")
	b.WriteString("  done\n")
	b.WriteString("  wait || true\n")
	b.WriteString("}\n")
	b.WriteString("trap 'stop_all; exit 0' TERM INT\n\n")

	if logs {
		b.WriteString("start_bg loki /otel-lgtm/loki/loki --config.file=" + ConfigDir + "/loki-config.yaml\n")
	}

	if metrics {
		b.WriteString("start_bg prometheus /otel-lgtm/prometheus/prometheus \\\n")
		b.WriteString("  --web.listen-address=127.0.0.1:9090 \\\n")
		b.WriteString("  --web.enable-remote-write-receiver \\\n")
		b.WriteString("  --web.enable-otlp-receiver \\\n")
		b.WriteString("  --enable-feature=exemplar-storage \\\n")
		b.WriteString("  --storage.tsdb.path=/data/prometheus \\\n")
		b.WriteString("  --config.file=/otel-lgtm/prometheus.yaml\n")
	}

	if traces {
		b.WriteString("start_bg tempo /otel-lgtm/tempo/tempo -config.file=" + ConfigDir + "/tempo-config.yaml\n")
	}

	b.WriteString("\nexport GF_PATHS_HOME=/data/grafana\n")
	b.WriteString("export GF_PATHS_DATA=/data/grafana/data\n")
	b.WriteString("export GF_PATHS_PLUGINS=/data/grafana/plugins\n")
	b.WriteString("export GF_SERVER_PROTOCOL=https\n")
	b.WriteString("export GF_SERVER_HTTP_PORT=3000\n")
	b.WriteString("export GF_SERVER_CERT_FILE=" + CertFile + "\n")
	b.WriteString("export GF_SERVER_CERT_KEY=" + KeyFile + "\n")
	b.WriteString("export GF_AUTH_ANONYMOUS_ENABLED=false\n")
	b.WriteString("export GF_SECURITY_ADMIN_USER=" + otelGrafanaUser + "\n")
	b.WriteString("export GF_SECURITY_ADMIN_PASSWORD=\"${BB_GRAFANA_PASSWORD}\"\n")
	b.WriteString("export GF_ANALYTICS_REPORTING_ENABLED=false\n")
	b.WriteString("export GF_ANALYTICS_CHECK_FOR_UPDATES=false\n")
	b.WriteString("export GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES=false\n")
	b.WriteString("export GF_LOG_LEVEL=warn\n")
	b.WriteString("start_bg grafana /otel-lgtm/grafana/bin/grafana server --homepath=/otel-lgtm/grafana\n\n")
	b.WriteString("start_bg otelcol /otel-lgtm/otelcol-contrib/otelcol-contrib --config=file:" + ConfigDir + "/otelcol-config.yaml\n\n")
	b.WriteString("while true; do\n")
	b.WriteString("  for i in \"${!pids[@]}\"; do\n")
	b.WriteString("    if ! kill -0 \"${pids[$i]}\" 2>/dev/null; then\n")
	b.WriteString("      echo \"otel: ${names[$i]} exited unexpectedly\" >&2\n")
	b.WriteString("      stop_all\n")
	b.WriteString("      exit 1\n")
	b.WriteString("    fi\n")
	b.WriteString("  done\n")
	b.WriteString("  sleep 1 &\n")
	b.WriteString("  wait $!\n")
	b.WriteString("done\n")

	return b.String()
}
