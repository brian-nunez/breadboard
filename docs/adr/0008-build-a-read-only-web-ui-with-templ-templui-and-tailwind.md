---
status: "superseded by ADR-0013"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Build a read-only web UI with templ, templUI and Tailwind

## Context and Problem Statement

Breadboard needs a web UI where learners read docs, see their architecture and inspect what they built. It ships inside the `bb` binary (ADR-0001) and wraps the core (ADR-0002). What should the UI be built with, and should it be able to change the system?

## Decision Drivers

* Assets must be embedded in a Go binary installed with `go install`, with no npm step for users or contributors.
* The UI is a learning tool, not a monitoring or control console.
* A local web endpoint that can drive Docker is reachable by malicious pages through DNS rebinding and cross-site requests.

## Considered Options

* Server-rendered templ with templUI components and Tailwind; read-only, with commands to copy
* A React single-page app built with Vite and embedded
* Server-rendered templ with action buttons that call the core

## Decision Outcome

Chosen option: "Server-rendered templ with templUI components and Tailwind; read-only, with commands to copy", because it embeds cleanly in the Go binary without npm, and making the UI read-only removes the riskiest attack surface while keeping the terminal as the single place changes happen.

Every action appears as a copyable terminal block (`bb add redis`). Live updates use SSE; the topology is server-rendered SVG. The accepted visual design (calm, minimal copy, no alerts) is in `docs/design/ui/`, and the Kafka overview and starter pages are the templates for all tech and starter pages.

### Consequences

* Good, because the UI needs no JavaScript build toolchain; Tailwind runs from its standalone CLI.
* Good, because a read-only UI cannot be used to start containers even if its protections fail.
* Good, because commands shown in the UI teach the CLI.
* Bad, because templ output and compiled CSS must be committed and checked for drift.
* Bad, because rich client interactions (graph panning, filtering) must be written with small amounts of vanilla JavaScript.
* Bad, because users switch to the terminal for every change.

### Confirmation

CI fails if `templ generate` or a Tailwind rebuild produces a diff. Review checks that web handlers only call read operations on the core. The UI binds to `127.0.0.1`, uses a per-launch token, and rejects foreign `Host` and `Origin` headers.

## Pros and Cons of the Options

### Server-rendered templ with templUI components and Tailwind; read-only

* Good, because it fits Go embedding and Brian's stack.
* Good, because the attack surface is small.
* Bad, because generated files are committed.

### A React single-page app built with Vite and embedded

* Good, because rich interactions are easier.
* Bad, because contributors need npm and a build step, and built assets must still be committed for `go install`.

### Server-rendered templ with action buttons that call the core

* Good, because users never leave the browser.
* Bad, because a local page that can start containers is a high-value target.
* Bad, because it duplicates the CLI's role as the place where changes happen.

## More Information

Recorded as D-005 and D-031 in `docs/design/decisions.md`. Design frames and the visual vocabulary are in `docs/design/ui/README.md`.
