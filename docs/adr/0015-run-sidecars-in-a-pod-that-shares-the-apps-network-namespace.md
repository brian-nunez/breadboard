---
status: "accepted"
date: 2026-11-16
decision-makers: Brian Nunez
---

# Run sidecars in a pod that shares the app's network namespace

## Context and Problem Statement

Phase 5 teaches the sidecar patterns: a proxy beside an app that terminates TLS for it (inbound), originates TLS for its calls (ambassador), captures all of its traffic (transparent proxy, the service-mesh data plane), a connection pooler on loopback, and a log shipper reading files the app writes (adapter). Each needs a container that sees the app's `localhost` and shares a directory with it. Breadboard already runs one such container, the Consul agent behind `register` (D-053), with `network_mode: service:<app>`. That shape cannot order startup: the agent can only start after the app, so a proxy the app depends on would come up too late, and transparent rules would be installed after the app's first packet. How should sidecars run, start, and be secured?

## Decision Drivers

* Real sidecars: the app talks to them over `127.0.0.1`, exactly as in Kubernetes, with no Breadboard SDK.
* Correct start order: rules, then sidecars, then the app, so the app never sends a packet that bypasses them.
* TLS stays on for every hop between containers (ADR-0005); plaintext only where it cannot leave one namespace.
* Least privilege: no long-running container holds `NET_ADMIN`.
* Experiments that use only `register` must not change.
* Everything stays plain Docker Compose (ADR-0003); exports run without `bb`.

## Considered Options

* A pod: a namespace holder container that owns the network, with the app and every sidecar joining it (`network_mode: service:<app>-pod`)
* Sidecars join the app's own namespace (`network_mode: service:<app>`), as the Consul agent does today
* Sidecars as separate services reached over the network (no shared namespace)

## Decision Outcome

Chosen option: "A pod", because it is the only option that both shares `localhost` and lets the app start after its sidecars, and it is the model Kubernetes uses, so the lesson transfers. (Brian's direction.)

* `apps.<name>.sidecars` lists sidecar entries; each `type` comes from a new **sidecar catalog** (`internal/catalog/sidecar`), separate from the infra catalog: `envoy`, `pgbouncer`, `fluent-bit`, and the internal `consul-agent`.
* An app with at least one sidecar becomes a pod. `<app>-pod` (`alpine:3.24.4`, `sleep infinity` under `init: true`, health check `true`, `16m`) owns the networks, aliases and published ports. The app (still named `<app>`), each sidecar (`<app>-<sidecar name>`, the name defaulting to the type) and, in transparent mode, `<app>-netinit` use `network_mode: service:<app>-pod`.
* Start order: pod (healthy) → netinit (completed successfully) → sidecars (healthy) → app. Every container in the pod mounts a shared volume at `/var/run/bb`; the app receives `BB_SHARED_DIR=/var/run/bb`.
* Loopback between an app and its own sidecars may be plaintext: it never leaves the shared namespace. Traffic between containers stays TLS with full verification. This extends the loopback exemption in `security.md`.
* Transparent mode is opt-in per Envoy sidecar. Its iptables rules are installed by `<app>-netinit`, a one-shot container built from a generated Dockerfile (`FROM alpine:3.24.4`, `apk add --no-cache iptables`), the only container granted `NET_ADMIN`, with `restart: "no"`. Envoy runs as uid 101 so the rules can exempt it.
* `register` keeps working unchanged and becomes the `consul-agent` sidecar type underneath. An app with only `register` keeps today's shape (`<app>-agent` with `network_mode: service:<app>`); with `sidecars` as well, the agent runs in the pod like any other sidecar.
* Sidecars present their app's certificate. Each sidecar type renders its container through one hook, `Render(sidecar.Input) (sidecar.Output, error)`, which returns the container (command, files, environment, health check), the variables it adds to or rewrites in the app's environment, the infra services it waits for, and an optional init container.

### Consequences

* Good, because an app reaches its proxy, pooler or shipper on `127.0.0.1` with standard client code, and its first packet already meets a ready sidecar.
* Good, because the pod is ordinary compose (`network_mode: service:`), so exported stacks run without `bb`.
* Good, because `NET_ADMIN` lives only in a container that exits after installing the rules.
* Good, because register-only experiments render byte-identical compose files, proved by golden tests.
* Bad, because each pod adds a holder container (`16m`), and the app's container no longer owns its network, so restarting the holder restarts the pod.
* Bad, because the pod holder is an implementation detail users must not see: status folds its state into the app's, and the UI and MCP do not list it.

### Confirmation

Golden tests render register-only experiments identical to `main`, and new fixtures cover a pod, replicas, and transparent mode. Integration tests against real Docker (Phase 5 wave A) prove Envoy inbound, outbound across replicas, transparent capture with no bypass when Envoy stops, PgBouncer over TLS, Fluent Bit shipping to a bucket, and unchanged registration.

## Pros and Cons of the Options

### A pod

* Good, because start order and shared `localhost` both hold.
* Good, because it mirrors Kubernetes pods and service-mesh sidecars.
* Bad, because one more container per instance.

### Sidecars join the app's own namespace

* Good, because no extra container, and it is what `register` does today.
* Bad, because a container can only join a namespace that already exists, so sidecars start after the app: the app's first calls fail, and transparent rules arrive late.

### Sidecars as separate services

* Good, because no namespace sharing at all.
* Bad, because app-to-sidecar traffic crosses the network and needs TLS, which removes the point of an ambassador or pooler on loopback, and transparent capture is impossible.

## More Information

Recorded as D-074 to D-078 and D-080 to D-084 in `docs/design/decisions.md`. Transparent mode was proven on Docker Desktop in a spike (Docker's embedded DNS kept working; with Envoy stopped the app could not reach its target). Replicas are [ADR-0016](0016-run-replicas-as-named-instances.md).
