---
status: "accepted"
date: 2026-11-15
decision-makers: Brian Nunez
---

# Fill bkit gaps with the standard library

Supersedes [ADR-0007](0007-use-bkit-modules-for-breadboard-internals.md).

## Context and Problem Statement

ADR-0007 chose bkit modules for Breadboard's internals and the standard library otherwise, but its outcome also said that where bkit lacks a capability (Kafka admin, Consul catalog), internals use the same official client libraries as the starters. Brian never approved that clause. It would make starter libraries such as franz-go and the Consul API direct dependencies of `bb` itself without a decision of their own. What should Breadboard's internals use when bkit has no matching module?

## Decision Drivers

* Standard library first; every direct dependency needs justification and Brian's approval.
* Starter code stays on official client libraries (ADR-0006), separate from Breadboard's internals.
* The host binary's dependency graph stays small and reviewed.

## Considered Options

* bkit where a module fits, the standard library otherwise; anything else needs its own approval
* bkit where a module fits, the starters' client libraries otherwise (ADR-0007 as written)

## Decision Outcome

Chosen option: "bkit where a module fits, the standard library otherwise; anything else needs its own approval", because it keeps every direct dependency of `bb` a deliberate, recorded decision.

Phase 1 uses `settings` (with the YAML driver) for install configuration and `gate` for spec and policy validation. The web UI phase adds `stash` with the in-memory driver for sessions, `rdb` with the Postgres driver for the Postgres explorer, and `objex` with the S3 driver for the bucket explorer. `experiment.yaml` is parsed directly with `go.yaml.in/yaml/v3`, because Breadboard needs strict decoding and comment-preserving node edits that `settings` does not offer. Where bkit has no matching capability, Breadboard uses the standard library. Any other direct dependency, including the client libraries starters use, needs Brian's approval and its own row in `docs/design/decisions.md`.

### Consequences

* Good, because configuration and validation share conventions with Brian's other projects.
* Good, because `gate.Join` reports every validation violation at once.
* Good, because no starter library becomes a dependency of `bb` by default.
* Bad, because a capability bkit lacks must be added to bkit (a separate release), built on the standard library, or approved as a new dependency.
* Bad, because Breadboard's release depends on bkit's published versions; `replace` directives are not allowed.

### Confirmation

Every direct dependency in `go.mod` is listed in `docs/design/decisions.md`. Review checks that new internal helpers do not duplicate a bkit module, that starters import no bkit package, and that no starter client library appears in `bb`'s own imports without an approved decision.

## Pros and Cons of the Options

### bkit where a module fits, the standard library otherwise; anything else needs its own approval

* Good, because every dependency of `bb` is reviewed on its own merits.
* Bad, because protocol features without a bkit module (for example, Kafka admin calls) need more work or an explicit approval.

### bkit where a module fits, the starters' client libraries otherwise

* Good, because the official libraries already speak each protocol.
* Bad, because large client libraries enter `bb`'s dependency graph without a decision.

## More Information

Recorded as D-009, D-018 and D-043 in `docs/design/decisions.md`. The module mapping is in the "Internal libraries (bkit)" section of `docs/design/architecture.md`. bkit: <https://github.com/brian-nunez/bkit>.
