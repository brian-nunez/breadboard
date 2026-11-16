---
status: "accepted"
date: 2026-11-16
decision-makers: Brian Nunez
---

# Run replicas as named instances

## Context and Problem Statement

Load-balancing and reverse-proxy examples (Phase 5) need several real instances of one app: a balancer must see each instance, health-check it, eject it, and report which one served a request. Compose can scale a service with `deploy.replicas`, but its containers share one service name, have generated container names, cannot carry per-instance configuration, and cannot each own a pod (ADR-0015). How should an app run more than one instance?

## Decision Drivers

* Each instance must be addressable on its own (for health checks, explorers, logs and certificates) and all of them by one shared name.
* TLS with full verification to any instance, by either name.
* Each instance may be a pod with its own sidecars.
* Experiments without replicas must not change.

## Considered Options

* Named instances: one compose service per instance, `<app>-1` … `<app>-N`
* Compose `deploy.replicas` on one service

## Decision Outcome

Chosen option: "Named instances", because each instance then has its own name, certificate, logs, status and pod, which a balancer lesson needs. (Brian's direction.)

* `apps.<name>.replicas: N` (1 to 10, default 1) renders N services `<app>-1` … `<app>-N`. Each is reachable as `<app>-<n>.<domain>` and as `<app>.<domain>`, a DNS alias on every instance, so the shared name resolves to all of them.
* Each instance's leaf certificate has DNS SANs for both names; instances share the app's SPIFFE identity.
* With replicas 1 (or absent) the output is unchanged: the service stays `<app>`.
* With sidecars, each instance is its own pod: `<app>-<n>-pod`, `<app>-<n>`, `<app>-<n>-envoy`, …; with `register`, each instance has its own agent `<app>-<n>-agent`.
* Edges and the env contract belong to the app, not to each instance; `bb logs <app>` streams every instance, and `bb logs <app>-<n>` one.
* An app with more than one replica cannot publish `ports` (the host port would collide); host access arrives with `*.localhost` routing (Phase 5 wave E).

### Consequences

* Good, because balancers (Envoy `STRICT_DNS` on the shared name, and later infra proxies) see real, separately addressable instances.
* Good, because each instance appears in `bb status` with its own health and resources.
* Bad, because the compose file grows linearly with replicas, and changing the count renames the service from `<app>` to `<app>-1` (compose removes the old container as an orphan).

### Confirmation

Golden tests cover replicas 3; core tests check instance names, the shared alias, certificate SANs and log selection; an integration test (Phase 5 wave A) proves requests through Envoy reach more than one instance.

## Pros and Cons of the Options

### Named instances

* Good, because every instance has a stable name, certificate and pod.
* Bad, because more compose services to render.

### Compose `deploy.replicas`

* Good, because one line in compose.
* Bad, because instances have no stable names, cannot have their own certificates or pods, and published ports collide.

## More Information

Recorded as D-079 in `docs/design/decisions.md`. Pods are [ADR-0015](0015-run-sidecars-in-a-pod-that-shares-the-apps-network-namespace.md).
