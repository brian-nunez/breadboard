# Starters

A starter takes the user from intent to running code: it writes a small, working project into the experiment's directory, adds the app services to `experiment.yaml`, and adds any infra services it needs that are missing. After that the code belongs to the user.

## Rules

- **Official client libraries only.** Starters import the real libraries and use them as their docs show. Breadboard ships no SDK or wrapper.
- **Bare minimum.** A starter is the least code that works and shows the concept: each app is one `main.go` that uses the library directly. No helper packages, no Breadboard plumbing, no code the lesson does not need. Comments explain concepts, not syntax.
- **Most popular wins. When choosing a starter's library, pick the one most of the ecosystem uses, so what the user learns transfers to real projects.
- **Configuration from env only.** Code reads the catalog env contract (`KAFKA_BROKERS`, `DATABASE_URL`, …). No Breadboard-specific config files.
- **Runs without Breadboard.** The project builds and runs anywhere the env vars and a trust bundle exist.
- **Containerised.** Each starter has a multi-stage `Dockerfile`:
  - `dev` stage: `golang:<exact>-alpine`, dependencies downloaded, then every app compiled with `go build -o /usr/local/bin/ ./cmd/...` while the image builds (a BuildKit cache mount keeps compiled packages between builds). Each app's `command` is its binary's name, so the program is the container's main process: it receives SIGTERM, and it is running, not compiling, when its health check passes. After editing code, `bb up` rebuilds the image and recreates the container.
  - `prod` stage: copies one binary from `dev` into a minimal non-root image, with labels `maintainer`, `version`, `description`, `platform`.
- **No health code.** Breadboard gives every app container a health check that its main process is running, so starters contain no health endpoints. An app that wants a readiness check opts in with `health` in `experiment.yaml`.
- **TLS through the environment.** Clients use the library's default TLS configuration, which verifies against `SSL_CERT_FILE`; no certificate paths appear in code.
- **Comments teach the why.** Explain concepts (consumer groups, offsets, at-least-once delivery, why invalidation follows the write), not the syntax.
- **Every starter README has two sections:** *Try this* (guided experiments) and *Break this* (failure drills: kill the broker, slow the consumer, exhaust memory).
- **Idempotent apply.** Re-applying a starter never overwrites user-modified files; it reports conflicts instead.
- **Unique app names.** App names are unique across every starter (a test enforces it), so several starters can share one experiment. Re-applying a starter to the same directory leaves its existing apps alone; if a name it needs belongs to anything else in `experiment.yaml`, `bb starter` fails before writing any file.

## Language and libraries

- v1 starters are **Go**, targeting Go **1.27.4** (`go 1.27.4` in each starter's `go.mod`).
- Default libraries (users may add or swap anything):

| Service | Library |
|---|---|
| Kafka | `github.com/twmb/franz-go` |
| Postgres | `github.com/jackc/pgx/v5` |
| Redis | `github.com/redis/go-redis/v9` |
| Consul | `github.com/hashicorp/consul/api` |
| S3 Compatible | `github.com/aws/aws-sdk-go-v2/service/s3` |

## Shape

A starter is one Go module; each app is a `cmd/<app>` entry, and each app service in the spec sets `command` to run its entry. Templates live in `starters/go/<area>/<name>/`, are embedded in the binary, and render with `text/template`. Every template file ends in `.tmpl` (including `go.mod` and `go.sum`), so the Go tool never treats starter code as part of Breadboard's module.

A starter declares the catalog types it needs; applying it adds any missing one (named after its type) and points each app's `uses` at the experiment's service of that type. The default target directory is the starter's name (`./producer-consumer`); `bb starter <id> --dir <path>` overrides it.

`kafka/producer-consumer` pins `github.com/twmb/franz-go v1.22.3` and its admin client module `github.com/twmb/franz-go/pkg/kadm v1.20.0`.

`kafka/sink-to-postgres` pins the same franz-go modules and `github.com/jackc/pgx/v5 v5.13.0`. Its apps are `sink-producer` (JSON order events on `order-events`) and `sink` (creates the `orders` table, inserts with `ON CONFLICT DO NOTHING`, and commits offsets only after the write).

`redis/cache-invalidation` pins `github.com/redis/go-redis/v9 v9.23.0` with the same pgx and franz-go modules. Its apps are `cache-writer` (updates Postgres, then announces on `product-updates`), `cache-reader` (cache-aside with a 30-second TTL) and `cache-invalidator` (deletes the changed key).

`s3/objects-basics` pins `github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10` and `github.com/aws/aws-sdk-go-v2/config v1.33.18`. Its one app, `objects`, runs the walkthrough once (bucket, prefix listing, multipart upload, presigned GET) and then waits so its logs stay readable. Path-style addressing is the only S3 setting in code.

`consul/service-discovery` pins `github.com/hashicorp/consul/api v1.35.0`. Its `greeter` serves HTTPS on 8443 with no Consul code; `register: {port: 8443}` in its spec entry makes Breadboard add the `greeter-agent` sidecar (see `catalog.md`). `caller` resolves healthy `greeter` instances through the Consul API and calls one. A starter app declares `register` through `starters.App.Register`, which `bb starter` writes into `experiment.yaml`.

`consul/self-registration` is the labeled alternative, with the same Consul API pin. `self-greeter` registers itself (address from `BB_FQDN`, a TLS check, `DeregisterCriticalServiceAfter` as a crash safety net) and deregisters on SIGTERM; like every starter app it runs its built binary as the main process, which is what lets it receive SIGTERM. Its README points to the recommended starter first.

### Sidecar starters

The `sidecar/*` starters teach the patterns of Phase 5's sidecars (see `catalog.md`, "Sidecar catalog"). A starter app declares replicas and sidecars through `starters.App.Replicas` and `starters.App.Sidecars`; a sidecar's reference to an infra service names its catalog type and resolves to the experiment's service of that type, like `uses`. Apps that must name another app derive the lab domain from `BB_FQDN` (everything after the first dot). No starter app contains TLS code: inbound Envoy sidecars terminate TLS, and calls between apps go through Envoy.

- `sidecar/envoy-ambassador` has no library pins (standard library only). `ambassador-backend` runs 3 replicas behind inbound Envoy sidecars and answers with its instance name; `ambassador-caller` calls `AMBASSADOR_BACKEND_URL` (its outbound sidecar, `lb: round_robin`, `timeout: 2s`, `retries: 2`) and logs a tally.
- `sidecar/envoy-tls-termination` has no library pins. `plain-web` serves plain HTTP on `BB_LISTEN_ADDR` behind an inbound Envoy sidecar; `tls-client` calls it over verified TLS and shows that the app's port is refused from another container.
- `sidecar/envoy-transparent` has no library pins. `transparent-caller` has a transparent Envoy sidecar with `egress: deny` and one declared outbound target; it calls `transparent-backend` (declared, TLS on the wire) and `transparent-stranger` (undeclared: the caller's sidecar closes the connection, counted in `egress_denied.rbac.denied`) by name in plain HTTP, and `transparent-backend`'s address with an undeclared `Host` (answered 403 by the sidecar's HTTP route).
- `sidecar/circuit-breaking` has no library pins. `breaker-backend` runs 3 replicas; `POST /fail` and `POST /recover` on a replica's loopback toggle 503s. `breaker-caller`'s outbound sets `retries: 0` and `outlier: {consecutive_5xx: 3, ejection: 30s, max_ejection_percent: 50, interval: 5s}`. Envoy's default `max_ejection_percent` (10%) would block ejecting one host of three; the README has the learner set it back to 10 to watch ejections overflow, and owning the generated config (`envoy.file`) is an optional exercise.
- `sidecar/pgbouncer-pool` pins `github.com/jackc/pgx/v5 v5.13.0`. `pool-client` opens 50 client connections (`pgxpool`) through a PgBouncer sidecar in transaction mode with `pool_size: 10`, and logs how many server connections Postgres sees.
- `sidecar/log-shipper` pins `github.com/aws/aws-sdk-go-v2 v1.48.0`, `github.com/aws/aws-sdk-go-v2/config v1.33.18` and `github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10`. `log-writer` writes JSON lines with `log/slog` to `BB_LOG_DIR` and has no S3 code; its Fluent Bit sidecar ships them to the `app-logs` bucket; `log-reader` lists the bucket with the AWS SDK.

## Starter list

| Id | Phase | Teaches |
|---|---|---|
| `kafka/producer-consumer` | 1 | Topics, partitions, keys, consumer groups, offsets, rebalancing. |
| `kafka/sink-to-postgres` | 2 | Consume → transform → write; idempotent inserts; at-least-once semantics; migrations. |
| `redis/cache-invalidation` | 2 | Cache-aside, invalidate-on-write via Kafka events, TTLs, stale reads. |
| `consul/service-discovery` (recommended) | 2 | Registration via a Consul sidecar, health-checked catalog, resolving services by name. |
| `consul/self-registration` (alternative) | 2 | The same lookup, with each service registering and deregistering itself through the Consul API: the plumbing the sidecar removes. |
| `s3/objects-basics` | 2 | Buckets, keys and prefixes, multipart upload, presigned URLs against S3 Compatible storage. |
| `sidecar/envoy-ambassador` | 5 | The ambassador pattern: client-side load balancing across replicas, retries and timeouts in an Envoy sidecar instead of app code. |
| `sidecar/envoy-tls-termination` | 5 | TLS termination at an Envoy sidecar, and why the app binds loopback only. |
| `sidecar/envoy-transparent` | 5 | Transparent proxying (iptables redirect, original destination), egress policy as the root of service ACLs, and how it differs from the ambassador. |
| `sidecar/circuit-breaking` | 5 | Envoy outlier detection: ejecting a failing replica, `max_ejection_percent`, ejection time and recovery. |
| `sidecar/pgbouncer-pool` | 5 | Connection pooling with a PgBouncer sidecar: transaction vs session mode, pool size vs client count. |
| `sidecar/log-shipper` | 5 | The adapter pattern: logs as files in a shared volume, shipped to S3 Compatible storage by a Fluent Bit sidecar. |
