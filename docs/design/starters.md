# Starters

A starter takes the user from intent to running code: it writes a small, working project into the experiment's directory, adds the app services to `experiment.yaml`, and adds any infra services it needs that are missing. After that the code belongs to the user.

## Rules

- **Official client libraries only.** Starters import the real libraries and use them as their docs show. Breadboard ships no SDK or wrapper.
- **Bare minimum.** A starter is the least code that works and shows the concept: each app is one `main.go` that uses the library directly. No helper packages, no Breadboard plumbing, no code the lesson does not need. Comments explain concepts, not syntax.
- **Most popular wins. When choosing a starter's library, pick the one most of the ecosystem uses, so what the user learns transfers to real projects.
- **Configuration from env only.** Code reads the catalog env contract (`KAFKA_BROKERS`, `DATABASE_URL`, …). No Breadboard-specific config files.
- **Runs without Breadboard.** The project builds and runs anywhere the env vars and a trust bundle exist.
- **Containerised.** Each starter has a multi-stage `Dockerfile`:
  - `dev` stage: `golang:<exact>-alpine`, `WORKDIR /src`, dependencies downloaded, source copied so the image runs on its own. `bb up` builds it, runs the app's `command`, and bind-mounts the source over `/src` with named volumes for the Go module and build caches.
  - `prod` stage: minimal non-root final image, with labels `maintainer`, `version`, `description`, `platform`.
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

## Starter list

| Id | Phase | Teaches |
|---|---|---|
| `kafka/producer-consumer` | 1 | Topics, partitions, keys, consumer groups, offsets, rebalancing. |
| `kafka/sink-to-postgres` | 2 | Consume → transform → write; idempotent inserts; at-least-once semantics; migrations. |
| `redis/cache-invalidation` | 2 | Cache-aside, invalidate-on-write via Kafka events, TTLs, stale reads. |
| `consul/service-discovery` | 2 | Registration via a Consul sidecar, health-checked catalog, resolving services by name. |
| `s3/objects-basics` | 2 | Buckets, keys and prefixes, multipart upload, presigned URLs against S3 Compatible storage. |
