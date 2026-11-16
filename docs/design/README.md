# Breadboard design

Breadboard (`bb`) is the source-of-truth design for this repository. Every session and agent works from these documents; when a decision changes, the change lands here and in [`decisions.md`](decisions.md) in the same commit.

## What Breadboard is

A local lab for practicing **real system design**. You pick infrastructure (Kafka, Postgres, Redis, Consul, …), and Breadboard runs the real services in Docker Compose on an isolated network, with TLS on by default, generated credentials, health checks, and resource limits. A **starter** drops working application code into your project that connects using the **official client libraries**, so you go from "I want to try Kafka consumers" to running code in minutes, then experiment from there.

When the experiment is worth keeping, Breadboard **exports** it: a standalone compose stack today, Kubernetes, Terraform, and Ansible later.

The name comes from electronics: on a breadboard you wire real components together to prove a circuit, then move it to a permanent board.

## Principles

1. **Real systems.** Every service is the real software in a container. No cloud emulators, no service mocks.
2. **Official client libraries.** Breadboard ships no SDK or wrapper. Starters use the libraries the ecosystem uses; the user installs whatever else they like.
3. **Every experiment runs in containers; `bb` itself does not.** `bb` is one binary on the host. Infrastructure and the user's app code are both compose services with `mem_limit`, `cpus`, a health check, explicit env vars, and `depends_on: service_healthy`.
4. **Secure by default, insecure by intent.** TLS is on for every hop between containers. Plaintext requires an explicit choice with a written reason, and stays visible.
5. **The user never handles a credential.** Breadboard generates, stores, and injects secrets; the user can always inspect them.
6. **Learning is part of the output.** Catalog entries and starters carry notes on what you got, what to try, and what to break.
7. **No lock-in.** Exported artifacts run without `bb` installed.
8. **Experiments only.** Breadboard optimises for local learning, not production hosting.

## Glossary

These words carry fixed meanings across the docs and the code.

| Term | Meaning |
|---|---|
| **experiment** | One isolated lab: a project directory holding `experiment.yaml` (the source of truth), app code, and the engine's `.bb/` state. One compose project, one network set. |
| **project state** | The `.bb/` directory beside `experiment.yaml`: generated secrets, certificates, runtime files. Never committed. |
| **catalog entry** | Breadboard's built-in definition of an infrastructure service type (`kafka`, `postgres`, …): image, config, listeners, health check, env contract, learning notes, exporter mappings. |
| **infra service** | An instance of a catalog entry inside an experiment. |
| **app service** | User code in the experiment, built from a directory with a `Dockerfile`, run as a compose service. |
| **replica** | One of an app's named instances, `<app>-<n>`, when `replicas` is above 1. Each has its own name and certificate; all answer to `<app>.<domain>`. |
| **sidecar** | A container from the sidecar catalog (Envoy, PgBouncer, Fluent Bit, the Consul agent) that shares one app instance's network namespace and certificate. |
| **pod** | An app instance with sidecars: a namespace holder (`<app>-pod`) that owns the network, joined by the app and its sidecars, started in order. |
| **starter** | A template that writes app code into the project and adds app services to `experiment.yaml`. |
| **listener** | A port an infra service accepts connections on, with a TLS mode. |
| **edge** | A directed connection from a client (app or infra service) to a listener. Derived from `uses`, Envoy outbound targets and proxy upstreams. TLS policy lives on edges. |
| **proxy** | An infra service whose catalog entry is a proxy type (HAProxy, …): it sits in front of its upstreams and decides TLS per hop. |
| **upstream** | One backend of a proxy (an app with all its replicas, or an infra service) and the edge to it, with a TLS mode: re-encrypt, terminate or passthrough. |
| **segment** | A compose network. Plaintext listeners live only on a private segment shared with their intended clients. |
| **resolved model** | The engine's internal, fully-defaulted representation of an experiment. Every output is generated from it. |
| **materialize** | Render the resolved model plus state into a concrete medium: compose files, `.env`, cert files, Kubernetes Secrets, Terraform variables. |
| **exporter** | A materializer targeting another platform (Kubernetes, Terraform, Ansible). |
| **identity** | A service's SPIFFE-style ID, `spiffe://<domain>/experiment/<exp>/service/<svc>`, embedded in its certificate. |
| **trust bundle** | The set of CA certificates a container trusts: public roots plus Breadboard CA(s). |

## Document map

| Document | Read when |
|---|---|
| [`architecture.md`](architecture.md) | Working on the engine, CLI, UI, MCP, state, runtime, or repo layout. |
| [`spec.md`](spec.md) | Touching `experiment.yaml` parsing, validation, or schema. |
| [`security.md`](security.md) | Touching PKI, TLS, listeners, edges, segments, secrets, or mTLS. |
| [`catalog.md`](catalog.md) | Adding or changing an infra service type or its env contract. |
| [`starters.md`](starters.md) | Writing or changing a starter. |
| [`exporters.md`](exporters.md) | Working on compose export, Kubernetes, Terraform, or Ansible output. |
| [`ui/README.md`](ui/README.md) | Working on the web UI (Phase 3): the accepted design, frames, and visual vocabulary. |
| [`roadmap.md`](roadmap.md) | Planning or executing a phase; checking what "done" means. |
| [`decisions.md`](decisions.md) | Before proposing a change to anything already decided; to see open questions. |
