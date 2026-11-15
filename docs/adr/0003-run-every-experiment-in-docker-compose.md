---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Run every experiment in Docker Compose

## Context and Problem Statement

An experiment combines infrastructure (Kafka, Postgres, Redis, Consul) with the user's own application code. Breadboard must run both reproducibly, with resource limits, health checks, networking and environment variables handled for the user. Where should infrastructure and user code run, and with what runtime?

## Decision Drivers

* Real services only; no emulators or mocks.
* Memory and CPU limits, health checks, ports and env vars must be enforced for every process, including user code.
* Service discovery practice requires apps to be reachable by name on a shared network.
* The user must be able to take the result and run it without Breadboard.

## Considered Options

* Infrastructure and user code as Docker Compose services
* Infrastructure in Compose, user code on the host
* Kubernetes (kind or k3d) locally
* Docker Engine API directly, without Compose

## Decision Outcome

Chosen option: "Infrastructure and user code as Docker Compose services", because it gives every process limits, health checks and a network identity, and the generated compose file is itself the portable artifact users keep.

Each experiment is one compose project (`bb-<name>`) on its own networks. Every service gets `mem_limit`, `cpus`, a health check, explicit env vars, `restart: unless-stopped` and `depends_on: condition: service_healthy`. Images are pinned to exact versions. Breadboard drives the `docker compose` CLI.

### Consequences

* Good, because user code is treated like any other service, so resource limits double as capacity-planning data.
* Good, because there is one address scheme (`<service>.<domain>`) and no host/container split for connection strings.
* Good, because `bb export compose` produces files that run with plain `docker compose`.
* Bad, because user code runs in a dev container with a bind mount, which can be slower on macOS than native builds.
* Bad, because Breadboard depends on the user's Docker and Compose versions.

### Confirmation

Golden-file tests cover the compose materializer for every catalog entry. The Phase 1 integration test brings up all entries on real Docker and checks health, and the exported stack must reach healthy without `bb`.

## Pros and Cons of the Options

### Infrastructure and user code as Docker Compose services

* Good, because limits and health checks apply uniformly.
* Good, because Compose is the format users already know and can keep.
* Bad, because bind-mounted builds are slower on macOS.

### Infrastructure in Compose, user code on the host

* Good, because user code builds and runs natively.
* Bad, because connection info differs between host and container, requiring dual listeners (notably for Kafka).
* Bad, because user code has no enforced limits or health checks and cannot take part in service discovery.

### Kubernetes (kind or k3d) locally

* Good, because it is closer to some production targets.
* Bad, because it adds heavy setup and operational concepts unrelated to the system being learned.
* Bad, because Brian's conventions reserve Kubernetes for workloads that need horizontal scaling.

### Docker Engine API directly, without Compose

* Good, because it allows finer control.
* Bad, because Breadboard would reimplement dependency ordering and health waiting.
* Bad, because there would be no portable artifact to hand the user.

## More Information

Recorded as D-001 and D-003 in `docs/design/decisions.md`. Runtime layout is in `docs/design/architecture.md`; service definitions are in `docs/design/catalog.md`.
