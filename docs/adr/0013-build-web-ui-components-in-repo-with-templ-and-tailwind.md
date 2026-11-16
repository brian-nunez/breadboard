---
status: "accepted"
date: 2026-11-16
decision-makers: Brian Nunez
---

# Build web UI components in-repo with templ and Tailwind

Supersedes [ADR-0008](0008-build-a-read-only-web-ui-with-templ-templui-and-tailwind.md).

## Context and Problem Statement

ADR-0008 chose a read-only web UI rendered with templ, using templUI components and Tailwind. templUI is copied into a repository as source, but its components call a shared helper that imports `github.com/Oudwins/tailwind-merge-go` to merge class names. Brian directed that `bb` keep its own dependencies minimal (D-059). The accepted design needs only a few simple components: cards, tables, badges, tabs, resource bars, a terminal block with Copy. Should the UI still use templUI?

## Decision Drivers

* Assets embedded in a Go binary installed with `go install`, with no npm step.
* `bb` keeps few dependencies; a class-merging library is not worth one.
* The UI is a learning tool, not a monitoring or control console, and stays read-only.
* A local web endpoint that can drive Docker is reachable by malicious pages through DNS rebinding and cross-site requests.

## Considered Options

* Server-rendered templ with in-repo components and Tailwind; read-only, with commands to copy
* Server-rendered templ with templUI components and Tailwind; read-only (ADR-0008 as written)

## Decision Outcome

Chosen option: "Server-rendered templ with in-repo components and Tailwind; read-only, with commands to copy", because the design's components are small enough to write directly, and doing so keeps `bb`'s web dependencies to templ itself.

Components live in `internal/web` as templ files styled with Tailwind utility classes that follow the design's visual vocabulary (slate neutrals, indigo for links and selection, status colours always with a word). Everything else from ADR-0008 stands: every action appears as a copyable terminal block, live updates use SSE, the topology is server-rendered SVG, Tailwind runs from its standalone CLI, and the UI is read-only.

### Consequences

* Good, because `bb` gains templ and nothing else for rendering.
* Good, because components carry exactly the classes the design uses, with no class-conflict resolution at runtime.
* Good, because a read-only UI cannot be used to start containers even if its protections fail.
* Bad, because components are written and maintained here instead of pulled from a kit.
* Bad, because templ output and compiled CSS must be committed and checked for drift.

### Confirmation

CI fails if `templ generate` or a Tailwind rebuild produces a diff. Review checks that web handlers only call read operations on the core and that `internal/web` imports no component kit. The UI binds to `127.0.0.1`, uses a per-launch token, and rejects foreign `Host` and `Origin` headers.

## Pros and Cons of the Options

### Server-rendered templ with in-repo components and Tailwind

* Good, because there is no extra dependency.
* Bad, because components are maintained in this repository.

### Server-rendered templ with templUI components and Tailwind

* Good, because components come ready-made.
* Bad, because its helpers add `tailwind-merge-go` to `bb`.

## More Information

Recorded as D-062 in `docs/design/decisions.md`; D-005 and D-031 now point here. Design frames and the visual vocabulary are in `docs/design/ui/README.md`.
