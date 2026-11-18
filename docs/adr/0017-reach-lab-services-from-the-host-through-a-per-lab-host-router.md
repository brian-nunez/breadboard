---
status: "accepted"
date: 2026-11-17
decision-makers: Brian Nunez
---

# Reach lab services from the host through a per-lab `*.localhost` router with a host-only CA

## Context and Problem Statement

A learner reaches a lab from the host today only through `apps.<name>.ports`, published on `127.0.0.1`. That breaks down in three ways. An app with replicas cannot publish ports, because the host port would collide (ADR-0016). Infra services such as a proxy, Consul's UI or S3 cannot be published at all. And a published port serves the lab certificate for `<service>.<domain>`, a name the host browser does not resolve and is not asked to trust. Browsers and curl resolve every `*.localhost` name to loopback (RFC 6761), so `https://<service>.<experiment>.localhost` can name a lab service on the host with no DNS setup. Two questions follow. How do host clients trust those names, given that the lab CA is name-constrained to the lab domain (ADR-0005)? And what listens on the host port?

## Decision Drivers

* Nothing existing changes: CAs, certificates, labs and compose output stay as they are for labs that do not opt in.
* Secure by default: every hop is TLS with verification; nothing is published unless the spec asks.
* A leaked key must not be able to vouch for a real site.
* Labs stay isolated: each one is its own compose project, with no component shared between labs.
* Apps with replicas, and infra HTTPS services, must be reachable.

## Considered Options

* A per-lab host router on its own loopback port, with a separate host-only CA name-constrained to `.localhost`
* A per-lab host router, with the lab CA's name constraints widened to `.localhost`
* One shared install-level router on `127.0.0.1:443` that joins every running lab

## Decision Outcome

Chosen option: "A per-lab host router on its own loopback port, with a separate host-only CA name-constrained to `.localhost`", because it leaves every existing CA, certificate and lab untouched, keeps each lab self-contained, and limits what the new CA key can sign to loopback names. (Brian's decision on the CA and on the topology.)

* **Host CA.** A second CA per install, named `host`, beside `root` in `<BB_HOME>/ca/`. Its name constraints (critical) let it sign only DNS names under `.localhost`: no URI, email address or IP address verifies under it (D-195), so it can never sign a SPIFFE identity or an address of a real host. It signs only the host router's host-facing certificate, with `serverAuth` only. Like `root`, it is never installed into a trust store automatically; `bb ca` shows it and how to trust it.
* **Host router.** An app or infra service opts in with `host` in `experiment.yaml`. A lab with at least one opted-in target gets one engine-owned service, `bb-host`, a Traefik container (the catalog's pinned image, file provider only, no Docker socket). It is published on `127.0.0.1:<port>`, where the port is chosen once per lab, recorded in `.bb/state.json`, and kept while it stays free. It terminates TLS with the host certificate, routes by `Host` to the target, and re-encrypts to every replica, verifying it against the lab CA. A lab without a `host` target renders exactly as before.
* **Scope.** HTTP over TLS only: apps with a TLS port and infra services with an HTTPS listener. TCP services such as Postgres, Redis and Kafka keep using `ports`.

### Consequences

* Good, because existing installs, labs and certificates are unaffected; the host CA is new state only.
* Good, because apps with replicas, proxies, Consul and S3 become reachable from a host browser with verified TLS.
* Good, because each lab owns its host router; `bb down` of one lab never affects another.
* Bad, because a learner who wants a browser to trust the host router must trust a second CA.
* Bad, because URLs carry a port (`https://api.lab.localhost:44312`), and the port can change if another program takes it while the lab is down.
* Bad, because clients that do not resolve `*.localhost` themselves (some system resolvers, for example macOS outside browsers and curl) need `--resolve` or an `/etc/hosts` line.
* Neutral, because the host router is Traefik, so the existing Traefik explorer reads its routes.

### Confirmation

Unit tests prove the host CA's constraint rejects any name outside `.localhost` and any URI SAN, and that the lab CA and lab certificates are byte-identical with or without host access. Golden tests prove a lab without `host` renders byte-identical to the previous release. An integration test reaches an app with three replicas and an infra HTTPS service through the host router, verifying its certificate against the host CA alone, and sees more than one replica answer.

## Pros and Cons of the Options

### A per-lab host router with a separate host-only CA

* Good, because no existing CA is re-minted and no lab is re-issued.
* Good, because the new key can sign only `*.localhost` names, which never leave the machine.
* Bad, because there are two CAs to explain and trust.

### A per-lab host router with the lab CA widened to `.localhost`

* Good, because one CA covers everything.
* Bad, because every install's CA must be re-minted and re-trusted, and every running lab re-issued.
* Bad, because a CA that already signs SPIFFE identities and client certificates would also vouch for host-facing names, widening what one key can do.

### One shared router on `127.0.0.1:443`

* Good, because URLs need no port.
* Bad, because it adds a component shared across labs, with its own lifecycle, that must join every lab's network.
* Bad, because binding port 443 fails with rootless Docker and conflicts with anything else on the host using it.

## More Information

Recorded as D-185 to D-194 in `docs/design/decisions.md`. The rules are in `docs/design/security.md` (host CA, the router's TLS), `docs/design/spec.md` (`host`) and `docs/design/architecture.md` (the router service, port allocation, status).
