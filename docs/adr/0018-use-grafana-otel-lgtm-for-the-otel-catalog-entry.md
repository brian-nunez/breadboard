---
status: "accepted"
date: 2026-11-20
decision-makers: Brian Nunez
---

# Use Grafana otel-lgtm for the `otel` catalog entry

## Context and Problem Statement

Breadboard teaches real distributed systems—load balancing, retries, circuit breakers, API gateways, Kafka pipelines and Postgres stores—but has no observability backend. Learners need to collect and inspect OpenTelemetry traces, metrics and structured logs emitted by official OpenTelemetry Go SDKs over verified TLS, without running and wiring five separate containers (collector, Tempo, Loki, Prometheus, Grafana) by hand. Which image and topology should back the `otel` catalog entry?

## Decision Drivers

* Every catalog entry is one container with an exact pinned official image, TLS by default verified against the lab CA, a real TLS health check, and bounded `mem_limit` and `cpus`.
* Learners should be able to enable only the telemetry signals they need (`logs`, `metrics`, `traces`) via service configuration (`config.signals`) or starter requirements, without running unused backends.
* Internal backend HTTP/gRPC endpoints (Loki, Tempo, Prometheus) must not be exposed unencrypted on the lab network.
* Starters and `bb add app` must use official OpenTelemetry Go packages (`go.opentelemetry.io/otel`, `go.opentelemetry.io/contrib/...`) configured via standard `OTEL_*` environment variables, with zero Breadboard SDK or wrapper.
* Grafana's UI should be reachable from the host when `host: true` is set, through the existing `bb-host` router (ADR-0017).

## Considered Options

* Single `grafana/otel-lgtm` container with generated collector/backend configs and selective signal startup
* Separate catalog entries (`otel-collector`, `tempo`, `loki`, `prometheus`, `grafana`) wired together in `experiment.yaml`
* Jaeger all-in-one (traces only)

## Decision Outcome

Chosen option: "Single `grafana/otel-lgtm` container with generated collector/backend configs and selective signal startup", pinned to `grafana/otel-lgtm:0.41.1`.

Breadboard mounts generated configs into the container:

* `otelcol-config.yaml` enables TLS on `0.0.0.0:4317` (OTLP gRPC) and `0.0.0.0:4318` (OTLP HTTP) with the service's lab certificate, binds the health check extension to `127.0.0.1:13133`, and includes only the pipelines listed in `config.signals` (`logs`, `metrics`, `traces`; default all three).
* `loki-config.yaml`, `tempo-config.yaml`, and Prometheus flags bind Loki (`3100`/`9095`), Tempo (`3200`/`9096`/`4418`), and Prometheus (`9090`) strictly to `127.0.0.1`, and our launcher starts only the backends whose signal is enabled (Pyroscope is never started).
* Grafana runs over HTTPS on `:3000` with the service's lab certificate, a generated admin password in `.bb/secrets.json` (`GF_AUTH_ANONYMOUS_ENABLED=false`), offline-safe settings (no plugin downloads or update checks on startup), and provisioned datasources matching only the enabled signals (with trace-to-logs and logs-to-trace correlation wired whenever the corresponding signals are enabled).
* Listeners declare `otlp-grpc` (4317) first so `uses: [otel]` creates an OTLP gRPC TLS edge, `grafana` (`https`, 3000) second so `host: true` routes `https://<name>.<experiment>.localhost:<port>` to Grafana, and `otlp-http` (4318) third.

### Consequences

* Good, because one `bb add otel` gives learners a complete, pre-wired OTLP + Grafana stack with a single volume (`/data`) and predictable resource limits (`1g`, `1.0` CPU).
* Good, because single-signal starters (`otel/traces`, `otel/metrics`, `otel/logs`) only run the OTel Collector, Grafana, and the one backend they need, and applying multiple `otel/*` starters to one lab unions their required signals on the shared `otel` service.
* Good, because all internal storage APIs stay strictly on `127.0.0.1`, reachable only by Grafana, the collector, and Breadboard's read-only Docker-exec explorer (`OtelTelemetry`, ADR-0012).
* Bad, because `grafana/otel-lgtm` is a larger image than single-purpose services (~934 MB compressed) and upgrades require verifying its bundled binary paths and config schemas.
* Bad, because internal components cannot be scaled independently in v1 (acceptable for a local learning lab).

### Confirmation

Unit tests verify `config.signals` validation, generated collector/Loki/Tempo/Grafana configs for every signal subset, and loopback bindings. Golden tests lock the compose bundle. Integration tests prove TLS verification on `4317`, `4318`, and `3000`, confirm `3100`/`3200`/`9090` are unreachable from other containers, confirm disabled backends are not running, and prove end-to-end ingestion and querying through `OtelTelemetry` and all four `otel/*` starters.

## More Information

Recorded in `docs/design/decisions.md` and `docs/design/catalog.md`. Tracks part of GitHub issue #108.
