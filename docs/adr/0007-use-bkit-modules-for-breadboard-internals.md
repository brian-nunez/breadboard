---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Use bkit modules for Breadboard internals

## Context and Problem Statement

Breadboard's own code needs layered configuration, rule-based validation, and later key-value sessions, database access for explorers and object storage access. Brian maintains bkit, a set of independently released Go modules for exactly these needs. Should Breadboard's internals use bkit, write their own helpers, or pull in other third-party libraries?

## Decision Drivers

* Standard library first; every direct dependency needs justification.
* Consistent APIs and sentinel errors across Brian's projects.
* Starter code must stay on official client libraries (ADR-0006), separate from Breadboard's internals.

## Considered Options

* bkit modules wherever one fits, standard library otherwise
* Hand-written helpers on the standard library only
* Popular third-party libraries per need

## Decision Outcome

Chosen option: "bkit modules wherever one fits, standard library otherwise", because bkit is standard-library-first, fully tested, uses sentinel errors and explicit drivers, and keeps Breadboard's dependency graph small.

Phase 1 uses `settings` (with the YAML driver) for install configuration and `gate` for spec and policy validation. The web UI phase adds `stash` with the in-memory driver for sessions, `rdb` with the Postgres driver for the Postgres explorer, and `objex` with the S3 driver for the bucket explorer. `experiment.yaml` is parsed directly with `go.yaml.in/yaml/v3`, because Breadboard needs strict decoding and comment-preserving node edits that `settings` does not offer. Where bkit has no matching capability, Breadboard uses the standard library; any other direct dependency, including the client libraries starters use, needs Brian's approval and its own row in `docs/design/decisions.md`.

### Consequences

* Good, because configuration and validation share conventions with Brian's other projects.
* Good, because `gate.Join` reports every validation violation at once.
* Bad, because a capability bkit lacks must either be added to bkit (a separate release) or built in Breadboard.
* Bad, because Breadboard's release depends on bkit's published versions; `replace` directives are not allowed.

### Confirmation

Every direct dependency is listed in `docs/design/decisions.md`. Review checks that new internal helpers do not duplicate a bkit module, and that starters import no bkit package.

## Pros and Cons of the Options

### bkit modules wherever one fits, standard library otherwise

* Good, because the modules are small, tested and consistent.
* Bad, because missing capabilities need a bkit change first.

### Hand-written helpers on the standard library only

* Good, because there are no external dependencies at all.
* Bad, because Breadboard would reimplement layered config and rule composition already solved in bkit.

### Popular third-party libraries per need

* Good, because they are widely known.
* Bad, because they add heavier dependency trees and inconsistent error handling.

## More Information

Recorded as D-009 and D-018 in `docs/design/decisions.md`. The module mapping is in the "Internal libraries (bkit)" section of `docs/design/architecture.md`. bkit: <https://github.com/brian-nunez/bkit>.
