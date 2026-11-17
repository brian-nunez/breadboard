# Breadboard (`bb`)

Local lab for real system design practice: real services in Docker Compose, TLS by default, starters using official client libraries, exporters to other platforms. Open source, for developers learning system design. Stage: Phases 1 (core engine and CLI), 2 (starters, S3 Compatible), 3 (web UI, `bb serve`) and 4 (MCP server, `bb mcp`) implemented; Phase 5 (proxies and relays) in progress: waves A (sidecars and replicas), B (proxies as services) and C (load-balancing and reverse-proxy examples) implemented.

## Design is the source of truth

`docs/design/` is authoritative. Start at [`docs/design/README.md`](docs/design/README.md) (principles, glossary, document map) before any design or implementation work, then read the documents its map names for your task.

- Check [`docs/design/decisions.md`](docs/design/decisions.md) before proposing a change to something already decided, and record every new decision there in the same change.
- When code and design disagree, stop and raise it with Brian; update whichever side is wrong.
- Brian approves each roadmap phase before it starts. A phase is done when its completion criteria in [`docs/design/roadmap.md`](docs/design/roadmap.md) have all been run and pass.

## Layout

- `docs/design/`: the design (architecture, spec, security, catalog, starters, exporters, roadmap, decision log).
- `docs/design/ui/`: the accepted web UI design. Edit `source/*.py` and run `source/build.sh`; never edit `breadboard-ui.figcraft` or `renders/` by hand.
- `docs/adr/`: architecture decision records.
- `docs/brand/`: icon and brand assets used by the README.
- Code (`bb/`, `internal/`, `starters/`) follows the repository layout in [`docs/design/architecture.md`](docs/design/architecture.md#repository-layout). New packages go where that layout puts them; new core capability goes in `internal/core` first.

## Conventions

- Go module `github.com/brian-nunez/breadboard`, toolchain `go1.27.4`.
- `bb` is one host binary (`go install github.com/brian-nunez/breadboard/bb@latest`). All capability lives in the core (`internal/core`); the CLI, web UI, and MCP server are thin wrappers over it. Generated code (templ output, compiled CSS) is committed and embedded.
- Internals: standard library first, then Brian's bkit modules as mapped in `docs/design/architecture.md`. Starters use official client libraries, never bkit. Every direct dependency appears in the decision log.
- No database for Breadboard's own state: `experiment.yaml` is the source of truth; generated state lives in files.
- Docker: exact image pins, mandatory health checks, `mem_limit`/`cpus` on every service, `restart: unless-stopped`.

## Commits

Every commit follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).

```text
<type>(<scope>): <summary>

<body>

<footers>
```

*   **Type**: `feat` (new behavior), `fix` (corrected behavior), `docs`, `refactor` (no behavior change), `perf`, `test`, `build`, `ci`, `style` (formatting only), `chore`, `revert`.
*   **Scope**: the area changed, from this project's areas: `core`, `cli`, `spec`, `model`, `catalog`, `pki`, `state`, `config`, `secrets`, `compose`, `runtime`, `starters`, `export`, `web`, `mcp`, `design`, `adr`. Omit it only when the change spans the whole repository.
*   **Summary**: imperative mood, lowercase, no trailing period, 72 characters or fewer. It names the change in behavior: `fix(pki): reject leaf names outside the domain`, not `fix(pki): update ca.go`.
*   **Body**: required unless the summary alone tells a reviewer everything. Wrap at 72 columns. State:
    *   **Why**: the problem, bug or need that prompted the change.
    *   **What**: the approach, and what now behaves differently.
    *   **Impact**: anything a reader must know: migrations, config changes, follow-ups, trade-offs taken.

    Every sentence carries information a reviewer cannot get from the diff. A body with nothing to add is left out.
*   **Footers**: `BREAKING CHANGE: <what breaks and how to migrate>` for incompatible changes (and `!` after the type or scope); `Refs: #<issue>` or `Closes: #<issue>` for tracked work.
*   One logical change per commit.

## Decisions

Decisions costly to reverse get a MADR record in [`docs/adr/`](docs/adr/README.md) before implementation, plus a row in `docs/design/decisions.md` linking to it. Smaller decisions are recorded only in `docs/design/decisions.md`.

## Boundaries

- Generated files are never edited by hand: `*_templ.go`, the compiled Tailwind stylesheet, and `docs/design/ui/breadboard-ui.figcraft` and `renders/`. Change the source and regenerate.
- An accepted ADR changes only its `status`; a changed decision gets a new ADR that supersedes it.
