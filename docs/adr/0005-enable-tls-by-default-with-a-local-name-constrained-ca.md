---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Enable TLS by default with a local name-constrained CA

## Context and Problem Statement

Breadboard exists for realistic system design practice, and real systems encrypt traffic between services. Configuring TLS in each client library is also a skill most developers only learn under pressure. Breadboard also needs to support proxy and relay experiments where a hop is deliberately plaintext. How should service-to-service transport security work?

## Decision Drivers

* Secure by default; insecure only by explicit, visible choice.
* Certificates must verify against real hostnames, not be skipped.
* A leaked local CA key must not threaten any real site.
* mTLS and multiple CAs must be possible later without reissuing everything.

## Considered Options

* TLS on every edge by default, from a local name-constrained CA, with per-edge opt-out that requires a reason
* Plaintext by default, with TLS as an opt-in per service
* TLS with self-signed certificates per service and verification disabled in clients

## Decision Outcome

Chosen option: "TLS on every edge by default, from a local name-constrained CA, with per-edge opt-out that requires a reason", because it is the only option that matches production practice, teaches real client TLS configuration, and still supports deliberate plaintext hops for proxy practice.

Breadboard creates one CA per install with Go's `crypto/x509`, name-constrained (critical) to the configured domain (default `bb.internal`). Every service gets a leaf certificate for `<service>.<domain>` with a SPIFFE identity URI and both `serverAuth` and `clientAuth` usages. Containers trust the CA through a bundle referenced by `SSL_CERT_FILE`. `tls.mode: off` on an edge requires a `reason`; the plaintext listener is placed on a private network shared only with the intended clients and is shown as a warning.

### Consequences

* Good, because health checks run over verified TLS, so "healthy" also proves the certificates work.
* Good, because starters show real TLS configuration in `pgx`, `go-redis`, `franz-go` and the Consul client.
* Good, because mTLS (Phase 6) becomes a policy change rather than a reissue.
* Bad, because each catalog entry must configure TLS on the server side, including awkward cases such as Kafka listeners and Postgres key ownership.
* Bad, because connection strings must always use the FQDN; bare service names fail verification.

### Confirmation

Unit tests prove the CA's name constraint rejects an out-of-domain leaf, and check leaf SANs, identity URI and key usages. Spec validation rejects `tls.mode: off` without a reason. The Phase 1 integration test connects to every service with full certificate verification.

## Pros and Cons of the Options

### TLS on every edge by default, from a local name-constrained CA

* Good, because it mirrors production.
* Good, because the name constraint limits the blast radius of the CA key.
* Bad, because it adds setup work to every catalog entry.

### Plaintext by default, with TLS as an opt-in per service

* Good, because it is simpler to build and debug.
* Bad, because learners would practice an insecure default.
* Bad, because per-service toggles cannot express proxy hops where one edge is encrypted and the next is not.

### TLS with self-signed certificates and verification disabled

* Good, because no CA is needed.
* Bad, because it teaches clients to skip verification, which is the habit Breadboard should break.

## More Information

Recorded as D-010 to D-014 in `docs/design/decisions.md`. Full rules, including proxy modes and mTLS extension points, are in `docs/design/security.md`.
