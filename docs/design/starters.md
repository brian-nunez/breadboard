# Starters

A starter takes the user from intent to running code: it writes a small, working project into the experiment's directory, adds the app services to `experiment.yaml`, and adds any infra services it needs that are missing. After that the code belongs to the user.

## Rules

- **Official client libraries only.** Starters import the real libraries and use them as their docs show. Breadboard ships no SDK or wrapper.
- **Most popular wins.** When choosing a starter's library, pick the one most of the ecosystem uses, so what the user learns transfers to real projects.
- **Configuration from env only.** Code reads the catalog env contract (`KAFKA_BROKERS`, `DATABASE_URL`, …). No Breadboard-specific config files.
- **Runs without Breadboard.** The project builds and runs anywhere the env vars and a trust bundle exist.
- **Containerised.** Each starter has a multi-stage `Dockerfile`:
  - `dev` stage: `golang:<exact>-alpine`, source bind-mounted, named volume for the Go module cache, runs the app's `command`.
  - `prod` stage: minimal non-root final image, with labels `maintainer`, `version`, `description`, `platform`.
- **Health.** Every long-running app serves `/healthz` on an internal port using `net/http`; its handler reflects real readiness (e.g. the Kafka client is connected).
- **TLS through the environment.** Clients use standard TLS configuration that verifies against `SSL_CERT_FILE`; comments explain each TLS setting.
- **Comments teach the why.** Explain concepts (consumer groups, offsets, at-least-once delivery, why invalidation follows the write), not the syntax.
- **Every starter README has two sections:** *Try this* (guided experiments) and *Break this* (failure drills: kill the broker, slow the consumer, exhaust memory).
- **Idempotent apply.** Re-applying a starter never overwrites user-modified files; it reports conflicts instead.

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

A starter is one Go module; each app is a `cmd/<app>` entry, and each app service in the spec sets `command` to run its entry. Templates live in `starters/go/<area>/<name>/`, are embedded in the binary, and render with `text/template`.

## Starter list

| Id | Phase | Teaches |
|---|---|---|
| `kafka/producer-consumer` | 1 | Topics, partitions, keys, consumer groups, offsets, rebalancing. |
| `kafka/sink-to-postgres` | 2 | Consume → transform → write; idempotent inserts; at-least-once semantics; migrations. |
| `redis/cache-invalidation` | 2 | Cache-aside, invalidate-on-write via Kafka events, TTLs, stale reads. |
| `consul/service-discovery` | 2 | Registration via a Consul sidecar, health-checked catalog, resolving services by name. |
| `s3/objects-basics` | 2 | Buckets, keys and prefixes, multipart upload, presigned URLs against S3 Compatible storage. |
