---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Ship bb as a single Go binary on the host

## Context and Problem Statement

Breadboard has three front ends (a CLI, a local web UI and an MCP server) that all drive Docker on the user's machine. They need a language, a packaging model and a place to run. An early design ran the CLI on the host and the UI and MCP server in containers, which forced the containers to mount the Docker socket, share state files across the host/VM boundary and translate bind-mount paths. How should Breadboard itself be built and distributed?

## Decision Drivers

* Installation must be one step for developers who already have Go and Docker.
* The UI and MCP server must not hold root-equivalent Docker access from inside a container.
* State files must not be shared between macOS and Docker Desktop's Linux VM.
* Brian's primary language is Go, and the tool shells out to Docker, which Go handles well.

## Considered Options

* One Go binary on the host containing the CLI, web UI and MCP server
* CLI on the host, web UI and MCP server in containers
* Everything, including the CLI, run from a container image
* A TypeScript/Next.js application

## Decision Outcome

Chosen option: "One Go binary on the host containing the CLI, web UI and MCP server", because it is the only option that installs with a single `go install`, keeps Docker socket access on the host where the user already has it, and avoids cross-boundary file sharing.

The main package lives at `bb/`, so the install command is `go install github.com/brian-nunez/breadboard/bb@latest`. The toolchain is pinned to Go 1.27.4.

### Consequences

* Good, because installation and upgrades are one command, with the version reported from `runtime/debug.ReadBuildInfo`.
* Good, because there is no Breadboard container image to build, publish or keep in sync.
* Good, because the UI and CLI share one process model and one set of state files.
* Bad, because `go install` runs no generators, so templ output and compiled CSS must be committed and checked for drift in CI.
* Bad, because the host process cannot join Docker networks, so UI explorers need another way to reach services (open question Q-11 in `docs/design/decisions.md`).
* Bad, because v1 supports only macOS and Linux hosts.

### Confirmation

A completion criterion in `docs/design/roadmap.md` installs the module path into an empty `GOPATH` and checks that `bb version` reports the module version. CI fails if regenerating templ output or the Tailwind stylesheet produces a diff.

## Pros and Cons of the Options

### One Go binary on the host containing the CLI, web UI and MCP server

* Good, because a single artifact carries every front end and the embedded assets.
* Good, because Docker access stays with the user who already has it.
* Neutral, because the web UI still needs localhost protections (bind to `127.0.0.1`, per-launch token, `Host`/`Origin` checks).
* Bad, because generated code must be committed.

### CLI on the host, web UI and MCP server in containers

* Good, because the UI could be updated as an image independently of the CLI.
* Bad, because the containers must mount the Docker socket, which is root-equivalent on the daemon's host.
* Bad, because state files would be written from both macOS and the Linux VM, where file locking is unreliable.
* Bad, because bind-mount paths must be identical inside and outside the container.

### Everything, including the CLI, run from a container image

* Good, because no Go toolchain is needed on the host.
* Bad, because every command needs `docker run` with the socket and project directory mounted.
* Bad, because it inherits every problem of the containerized UI option.

### A TypeScript/Next.js application

* Good, because the web UI ecosystem is large.
* Bad, because distribution needs a Node runtime and a package step instead of a single binary.
* Bad, because it is not Brian's primary language for tooling.

## More Information

Recorded as D-004, D-016 (superseded), D-024 and D-027 in `docs/design/decisions.md`. Layout and distribution details are in `docs/design/architecture.md`.
