---
status: "accepted"
date: 2026-11-16
decision-makers: Brian Nunez
---

# Serve MCP with the official Go SDK

## Context and Problem Statement

Phase 4 adds `bb mcp`, a stdio MCP server whose tools wrap the core so AI agents can build, run and debug experiments. MCP keeps changing: the 2026-07-28 revision made the protocol stateless (no `initialize` handshake, the protocol version in every request's `_meta`, a mandatory `server/discover`), and a server that speaks only that revision fails against clients still on 2025-11-25 or earlier. How should `bb` implement the protocol?

## Decision Drivers

* Work with the MCP clients people use today, on either side of the 2026-07-28 change.
* Keep up with later revisions without Breadboard maintaining protocol code.
* `bb` keeps few direct dependencies; each needs Brian's approval (ADR-0011).
* Tool input and output schemas should match the Go types the handlers use.

## Considered Options

* The official Go SDK, `github.com/modelcontextprotocol/go-sdk`
* JSON-RPC over stdio on the standard library, written in-repo

## Decision Outcome

Chosen option: "The official Go SDK", because it is maintained alongside the specification, already negotiates 2026-07-28 and every earlier version on one server, and derives each tool's JSON Schema from its Go input and output types and validates against them. (Brian's direction.)

`internal/mcp` registers one tool per core operation with the SDK's typed `AddTool`; input and output are MCP-side view types with `snake_case` JSON names, so the core's result types stay free of wire concerns. `bb mcp` serves over the SDK's stdio transport. Upgrading the SDK is how `bb` follows new protocol revisions.

### Consequences

* Good, because legacy clients (`initialize`) and 2026-07-28 clients (`server/discover`, per-request versions) both work, verified with both against the real binary.
* Good, because input validation, structured content with a JSON text copy, progress notifications and cancellation come from the SDK.
* Bad, because the SDK brings transitive modules `bb` does not use at runtime (OAuth, JWT, URI templates for HTTP transports); only the packages the stdio server imports are linked.
* Bad, because the SDK's README makes no v1 compatibility promise and removes deprecated options on a schedule, so upgrades need reading the release notes.

### Confirmation

Unit tests drive the server through the SDK's in-memory transports; an integration test drives the built `bb` binary over stdio against real Docker. The SDK version appears in `go.mod` and in the decision log.

## Pros and Cons of the Options

### The official Go SDK

* Good, because protocol changes land upstream, often before clients adopt them.
* Good, because schemas come from Go types, so tools cannot drift from their documented input.
* Bad, because it is a large dependency for a small surface.

### JSON-RPC over stdio on the standard library

* Good, because `bb` would gain no dependency.
* Bad, because Breadboard would carry two protocol eras, version negotiation and every later revision itself.

## More Information

Recorded as D-070 in `docs/design/decisions.md`. Specification: <https://modelcontextprotocol.io/specification/2026-07-28>. SDK: <https://github.com/modelcontextprotocol/go-sdk> (v1.8.0).
