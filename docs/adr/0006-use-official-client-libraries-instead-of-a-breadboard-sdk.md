---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Use official client libraries instead of a Breadboard SDK

## Context and Problem Statement

Starters give users working application code that connects to the services in their experiment. Hosted platforms often ship their own SDKs that wrap clients and hide configuration. Should Breadboard provide an SDK, or should starter code use the libraries the ecosystem uses?

## Decision Drivers

* What users learn must transfer to real projects.
* Starter code must run without Breadboard installed.
* Breadboard must not take on maintenance of client wrappers for every language and service.

## Considered Options

* Official, most popular client libraries configured through environment variables
* A Breadboard SDK that wraps each service's client
* A thin helper library that only reads Breadboard's configuration

## Decision Outcome

Chosen option: "Official, most popular client libraries configured through environment variables", because it is the only option where the code users write is the code they would write anywhere else.

Starters read each catalog entry's env contract (`KAFKA_BROKERS`, `DATABASE_URL`, `REDIS_URL`, …) and use the most popular library in the ecosystem. The Go defaults are `franz-go`, `pgx/v5`, `go-redis/v9`, `hashicorp/consul/api` and `aws-sdk-go-v2`. Users can add or swap any library.

### Consequences

* Good, because starter code runs anywhere the env vars and a trust bundle exist.
* Good, because adding a starter language needs no new Breadboard library.
* Good, because users read each library's own documentation, not Breadboard's.
* Bad, because starters must show some boilerplate (TLS configuration, connection setup) that an SDK would hide; comments explain it.
* Bad, because library API changes can break starters, so starters need build checks.

### Confirmation

Rendered starters must pass `go build ./...` and `go vet ./...` in CI. Review checks that starters import no Breadboard package and read configuration only from the env contract.

## Pros and Cons of the Options

### Official, most popular client libraries configured through environment variables

* Good, because skills transfer directly.
* Good, because there is no lock-in.
* Bad, because starter code is more verbose.

### A Breadboard SDK that wraps each service's client

* Good, because starter code would be shorter.
* Bad, because users would learn Breadboard's API instead of the real one.
* Bad, because the SDK would need maintaining per language and per service.

### A thin helper library that only reads Breadboard's configuration

* Good, because it removes env-var parsing.
* Bad, because starter code would still depend on Breadboard to run.

## More Information

Recorded as D-002, D-008 and D-029 in `docs/design/decisions.md`. Starter rules are in `docs/design/starters.md`.
