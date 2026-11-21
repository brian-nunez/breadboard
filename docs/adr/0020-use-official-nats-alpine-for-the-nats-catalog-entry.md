---
status: "accepted"
date: 2026-11-21
decision-makers: Brian Nunez
---

# Use official NATS Alpine image for the `nats` catalog entry

## Context and Problem Statement

Breadboard ships Kafka for partitioned commit-log streaming, Redis for in-memory caching/pub-sub, and Consul for service discovery, but lacks a lightweight, low-memory messaging and coordination engine supporting subject hierarchies, queue groups, request-reply RPC, JetStream work queues, and reactive Key-Value buckets. Which image, security setup, and configuration should back the `nats` catalog entry?

## Decision Drivers

* Every catalog entry is one container with an exact pinned official image, TLS by default verified against the lab CA, a real health check, and bounded `mem_limit` and `cpus`.
* The container must support Breadboard's read-only Docker-exec explorer (ADR-0012) via loopback monitoring without exposing monitoring ports on the lab network.
* Credentials must come from `.bb/secrets.json` without writing literal secrets into generated config files so `dotenv` exports remain secret-free.
* Learners should be able to run both Core NATS (`config.jetstream=false`) and JetStream persistence (`config.jetstream=true`, default).
* Starters and `bb add app` must use the official Go client (`github.com/nats-io/nats.go` and its `jetstream` package) configured purely from environment variables, verifying TLS through `SSL_CERT_FILE`.

## Considered Options

* Official `nats:2.15.3-alpine3.22` image with TLS on `4222`, `--auth` token, loopback HTTP monitoring on `127.0.0.1:8222`, and configurable JetStream
* Official `nats:2.15.3` scratch image (no shell, cannot run Docker-exec probes or expand `$NATS_TOKEN` in entrypoint)
* Separate NATS + `nats-box` sidecar container

## Decision Outcome

Chosen option: "Official `nats:2.15.3-alpine3.22` image with TLS on `4222`, `--auth` token, loopback HTTP monitoring on `127.0.0.1:8222`, and configurable JetStream".

Breadboard mounts generated `/etc/bb/config/nats.conf`:

* Client listener binds `0.0.0.0:4222` with `tls { cert_file, key_file, ca_file }` mandatory (no plaintext listener; `tls.mode: off` is refused).
* Monitoring binds strictly to `127.0.0.1:8222` (plain HTTP on loopback only, unreachable from other containers), used by the health check (`/healthz` + `/varz` verifying `tls_required`) and the `NatsOverview` read-only Docker-exec explorer (`/varz` and `/jsz?streams=true&consumers=true&config=true`).
* Token auth uses a generated `token` secret passed via `--auth "$NATS_TOKEN"` in the container entrypoint so `nats.conf` holds no secret.
* `config.jetstream` (`true` default, or `false`) toggles the `jetstream` block (`store_dir: /data`, `max_memory_store: 64MB`, `max_file_store: 512MB`).
* Resource defaults are `128m` memory and `0.5` CPU, highlighting NATS's small footprint compared to JVM brokers.

### Consequences

* Good, because `bb add nats` starts in under a second with `128m` RAM, verified TLS on `4222`, token authentication, and persistent JetStream enabled out of the box.
* Good, because the Alpine variant includes `sh` and `wget`, allowing clean Docker-exec exploration without adding a second container.
* Good, because `nats.conf` stays secret-free and works identically in `inline` and `dotenv` export modes.
* Bad, because NATS's Alpine image does not bundle `openssl` or the `nats` CLI; TLS handshake verification is proven directly by client connections (`nats.go` over `tls://`) and `nats-server`'s strict certificate load + `/varz` `tls_required: true` check.

### Confirmation

Unit tests verify `config.jetstream` validation, generated `nats.conf` (TLS block, loopback monitoring, JetStream toggle), secret-free config files, and client env vars. Integration tests start `nats:2.15.3-alpine3.22`, prove TLS verification and token auth via `bb add app` (`newNats`), confirm unauthenticated connections fail, confirm monitoring port `8222` is unreachable from other containers, and verify `NatsOverview` and all three `nats/*` starters end-to-end.

## More Information

Recorded in `docs/design/decisions.md` and `docs/design/catalog.md`. Completes GitHub issue #108.
