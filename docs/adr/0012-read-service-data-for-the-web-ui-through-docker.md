---
status: "accepted"
date: 2026-11-16
decision-makers: Brian Nunez
---

# Read service data for the web UI through Docker

Resolves open question Q-11.

## Context and Problem Statement

The Phase 3 web UI (`bb serve`) shows what each service holds: Kafka topics, consumer groups and lag; Postgres tables and row counts; Redis keys; the Consul catalog; S3 buckets. `bb` runs on the host, while the services listen only on the experiment's private Docker network, behind TLS certificates for names like `kafka.bb.internal` that the host cannot resolve. How does `bb` read that data?

## Decision Drivers

* `bb` itself keeps few dependencies. Popular client libraries belong in starters, not in the host binary (ADR-0011).
* No new attack surface on the host, and no change to how services listen.
* Works on macOS and Linux with only Docker.
* Kafka clients reconnect to the address the broker advertises, which is a lab-internal name.

## Considered Options

* `docker exec` each service's own command-line tools
* Publish a management port per service on `127.0.0.1` and connect with client libraries
* A short-lived helper container on the experiment's network

## Decision Outcome

Chosen option: "`docker exec` each service's own command-line tools", because every catalog image already ships the tools that answer the UI's questions, it adds no dependency to `bb` and no listener to the host, and the tools run inside the network where the lab names resolve and the lab CA is trusted.

The core gains read-only explorer operations. Each runs one command through the runtime's exec, over TLS with the mounted CA where the service requires it, and parses its output into typed results: `kafka-topics.sh` and `kafka-consumer-groups.sh` for Kafka, `psql` for Postgres, `redis-cli --scan` for Redis, the `consul` CLI for Consul, and `curl --aws-sigv4` for S3 Compatible. Status keeps coming from `docker inspect` and `docker stats`. A service that only speaks HTTP may instead be reached through a port published on the host, if one is ever needed.

### Consequences

* Good, because `bb` gains no client libraries; templ (ADR-0008) and bkit `stash` for sessions are the web UI's only new dependencies.
* Good, because nothing new listens on the host, and services keep their TLS-only network posture.
* Good, because the Kafka advertised-listener problem disappears: the tools run where the advertised name resolves.
* Bad, because explorer results depend on each tool's text output, which can change between image versions; parsers are covered by tests against fixed output and by integration tests against the pinned images.
* Bad, because each explorer request starts a process in the container, so the UI caches results briefly instead of polling them continuously.

### Confirmation

Integration tests run every explorer against real containers of the pinned images. Review checks that explorer operations are read-only.

## Pros and Cons of the Options

### `docker exec` each service's own command-line tools

* Good, because the tools exist in every image and speak the protocol natively.
* Bad, because output parsing is tied to tool versions.

### Publish a management port per service on `127.0.0.1`

* Good, because results come from structured protocol responses.
* Bad, because `bb` would need a client library per protocol, and Kafka would need a second listener advertising a host port fixed before the broker starts.

### A short-lived helper container on the experiment's network

* Good, because the helper can carry any tool.
* Bad, because it is another image to pin, pull and keep current, for tools the service images already have.

## More Information

Recorded as D-059 in `docs/design/decisions.md`. Brian's direction: `bb` reaches services through Docker and keeps its own dependencies minimal.
