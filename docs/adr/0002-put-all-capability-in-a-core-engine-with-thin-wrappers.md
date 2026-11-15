---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Put all capability in a core engine with thin wrappers

## Context and Problem Statement

Breadboard is used from a CLI, a web UI and an MCP server. If each front end implements its own logic, they drift: the CLI gains a feature the UI lacks, or two front ends validate a spec differently. Where does Breadboard's behavior live, and what are the front ends allowed to do?

## Decision Drivers

* Every capability must be available identically from the CLI, the web UI and MCP.
* The engine must be testable without a terminal, a browser or Docker.
* Phase 1 ships only the CLI; later front ends must not require reworking the engine.

## Considered Options

* A core engine API with the CLI, web UI and MCP server as thin wrappers
* The CLI as the primary implementation, with the UI and MCP server shelling out to `bb`
* Separate implementations per front end sharing utility packages

## Decision Outcome

Chosen option: "A core engine API with the CLI, web UI and MCP server as thin wrappers", because it is the only option where a capability written once is available everywhere and where the engine can be tested through its own API.

The core (`internal/core`) exposes one method per capability. Each takes `context.Context` and a request struct and returns a result struct and an error. Long operations report progress through an event sink, streams are `iter.Seq2[T, error]`, failures are sentinel errors, and Docker sits behind an internal interface. The core never prints, reads stdin, parses flags, opens a browser or exits.

### Consequences

* Good, because the web UI and MCP phases add no domain logic; a missing capability becomes a core operation first.
* Good, because core tests run with a fake Docker runtime and no I/O.
* Good, because each wrapper decides how to render events: the CLI prints them, the UI streams them over SSE, MCP collects them.
* Bad, because every operation needs request, result and event types even when only the CLI uses it today.
* Bad, because the wrapper boundary must be guarded in review and by tests, or logic leaks into the CLI.

### Confirmation

Phase 1 completion criteria require every core operation to be tested through the core API with a fake Docker runtime, and a test or lint rule that fails if `internal/core` or anything below it uses `fmt.Print*`, `os.Exit`, `os.Stdin` or `flag`.

## Pros and Cons of the Options

### A core engine API with the CLI, web UI and MCP server as thin wrappers

* Good, because behavior is defined in one place.
* Good, because the API is typed and testable.
* Bad, because it front-loads API design work.

### The CLI as the primary implementation, with the UI and MCP server shelling out to `bb`

* Good, because the CLI is built first anyway.
* Bad, because the UI and MCP would parse CLI text output, which is fragile.
* Bad, because progress and streaming would be limited to what the terminal format carries.

### Separate implementations per front end sharing utility packages

* Good, because each front end can optimize its own flow.
* Bad, because front ends drift apart over time.
* Bad, because tests must cover the same behavior several times.

## More Information

Recorded as D-028 in `docs/design/decisions.md`. The operation list is in the "Core API" section of `docs/design/architecture.md`.
