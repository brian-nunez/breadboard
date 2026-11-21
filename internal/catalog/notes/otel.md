# OpenTelemetry (LGTM)

## What you got

An OpenTelemetry Collector bundled with Grafana, Loki (logs), Prometheus (metrics), and Tempo (traces) in one container (`grafana/otel-lgtm`). The network listeners are OTLP gRPC on port `4317`, OTLP HTTP on port `4318`, and Grafana HTTPS on port `3000`, all encrypted with the service's lab certificate and verified against the lab CA. Internal backend APIs (Loki `3100`, Tempo `3200`, Prometheus `9090`) bind to `127.0.0.1` only.

Only the signals listed in `config.signals` (default `logs,metrics,traces`) are started and wired in the collector and Grafana. Apps that `uses: [otel]` receive standard `OTEL_*` variables (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_CERTIFICATE`, and per-signal `OTEL_*_EXPORTER` flags), so the official OpenTelemetry Go SDK connects over verified TLS with no custom certificate code.

## Key concepts

- **Three correlated signals.** Traces follow one request across services (`trace_id`, parent/child spans); metrics aggregate rates, errors, and duration histograms; structured logs record individual events carrying the active `trace_id` and `span_id`.
- **Context propagation.** Passing `traceparent` in HTTP headers (via `otelhttp`) or Kafka record headers joins separate processes into one distributed trace.
- **Resource identity.** Every signal carries `service.name` and `service.instance.id` so Grafana, Tempo, Prometheus, and Loki can separate services and replicas.
- **Host access to Grafana.** Set `host: true` on `otel` in `experiment.yaml` (enabled by default in the `otel/*` starters) to open Grafana in your browser at `https://otel.<experiment>.localhost:<port>`. Run `bb creds otel` for the generated admin password.

## Try this

- Start a single-signal backend with `bb add otel --config signals=traces` and confirm only Tempo, Grafana, and the collector traces pipeline run.
- Open Grafana via the host URL shown by `bb status`, sign in with `bb creds otel`, and jump from a Tempo trace directly to its correlated Loki logs.
- Use the **Telemetry** tab in `bb serve` to inspect recent traces, metric names, and log entries read through Docker.

## Break this

- Remove `otel.SetTextMapPropagator` from one service in a chain and watch one trace split into disconnected root traces in Tempo.
- Omit `Shutdown` on `TracerProvider` or `LoggerProvider` before exit and watch the final batch of spans or logs disappear before reaching the collector.
- Point an exporter at `4317` with `WithInsecure()` and watch the TLS handshake fail.
