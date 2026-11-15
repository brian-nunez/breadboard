# Architecture

## Components

**The core is the product.** Every capability lives in the core engine (`internal/core`). The CLI, the web UI, and the MCP server are **wrappers**: they translate their input into a core call and render the result. A front end holds no domain logic, so any capability the CLI has, the web UI and MCP server get by wrapping the same call.

One Go binary, `bb`, runs on the host and contains the core and all three wrappers. Breadboard itself never runs in a container: only the experiments do.

```
              ┌──────────────── bb (one binary, host) ────────────────┐
              │   CLI            Web UI (bb ui)      MCP (bb mcp)     │
              │     └───────────────┼───────────────────┘             │
              │                     ▼                                 │
experiment ──►│   engine: spec → resolved model → materialize         │
  .yaml       └─────────────────────┬─────────────────────────────────┘
 (intent)                           ▼
              .bb/ (secrets, certs, compose files)  ──►  docker compose  ──►  Docker
              (later) Kubernetes / Terraform / Ansible
```

| Wrapper | Started by | Phase | Role |
|---|---|---|---|
| CLI | `bb <command>` | 1 | Parses flags, calls the core, prints results and progress. |
| Web UI | `bb ui` | 3 | Local web server on `127.0.0.1`: topology, health, logs, explorers, learning notes. templ + templUI + Tailwind, embedded in the binary. |
| MCP server | `bb mcp` | 4 | stdio MCP server exposing core operations as tools. |

## Core API

The core is a Go API with one method per capability. Its contract:

- **Typed in, typed out.** Each operation takes `context.Context` and a request struct, and returns a result struct and an error. Results are data (service states, credentials, warnings), never preformatted text.
- **Progress as events.** Long operations (`Up`, `Down`, `ApplyStarter`, `ExportCompose`) report progress through an event sink passed in the request: steps started and finished, health transitions, warnings. The CLI prints events, the web UI streams them over SSE, the MCP server collects them into its tool result.
- **Streams as iterators.** Logs and similar streams are returned as `iter.Seq2[T, error]` and stop when the context is cancelled.
- **Errors are sentinels.** Every failure a wrapper must react to (`ErrNotInitialized`, `ErrBusy`, `ErrValidation`, `ErrNewerFormat`, `ErrDockerUnavailable`, …) is a sentinel matched with `errors.Is`; validation errors carry every violation with its YAML path.
- **No I/O to the user.** The core never prints, reads stdin, parses flags, opens a browser, or exits the process.
- **Docker behind an interface.** The core talks to Docker through an internal interface, with the `docker compose` CLI implementation in `internal/runtime`, so core tests run without Docker and integration tests run with it.

Operations (Phase 1):

| Operation | Purpose |
|---|---|
| `Init` | Create `experiment.yaml` and `.bb/`; create `BB_HOME` and the CA if missing. |
| `Catalog`, `Learn` | List catalog entries; return an entry's learning notes. |
| `AddService`, `RemoveService` | Edit `experiment.yaml`. |
| `Starters`, `ApplyStarter` | List starters; write starter code and update the spec. |
| `Up`, `Down` | Reconcile, materialize, run, wait for health; stop and optionally delete data. |
| `Status`, `Logs` | Per-service state, health, resources, TLS warnings; log stream. |
| `Creds`, `CA` | Generated credentials and connection strings; CA details. |
| `ConfigGet`, `ConfigSet` | Install settings. |
| `Migrate` | Upgrade `experiment.yaml` to the current spec version. |
| `ExportCompose` | Write a standalone compose stack. |
| `Version` | Build version. |

The web UI and MCP phases add no core logic of their own; if a wrapper needs something new, it becomes a core operation first.

## Distribution

- Installed with `go install github.com/brian-nunez/breadboard/bb@latest`. The main package lives in the repository's `bb/` directory. No container image, no separate assets.
- `go install` runs no code generators, so everything generated is committed and embedded:
  - templ output (`*_templ.go`) is committed; CI fails if `templ generate` produces a diff.
  - The compiled Tailwind stylesheet is committed and embedded with `go:embed`; CI fails if a rebuild produces a diff. Tailwind runs from its standalone CLI, so contributors need no npm.
  - templUI components are copied into the repo (templUI's model), so they build with the module.
  - Starter templates and learning notes are embedded with `go:embed`.
- `bb version` reads the module version from `runtime/debug.ReadBuildInfo`, so `go install …@vX.Y.Z` reports the right version without build flags.
- Supported hosts for v1: macOS and Linux.

## Pipeline

1. **Load** `experiment.yaml` ([`spec.md`](spec.md)), the source of truth for the experiment. Validate with `gate` checks.
2. **Resolve** the spec against the catalog into the resolved model: defaults applied, edges derived from `uses`, listeners and segments computed, TLS policy evaluated ([`security.md`](security.md)).
3. **Reconcile state**: ensure the CA, secrets, and leaf certificates exist; issue or reissue as needed.
4. **Materialize** into a target: the runtime compose project, an export, or (later) another platform.
5. **Run** (runtime target only): `docker compose -p bb-<experiment> -f … up -d`, then wait for health.

Every output comes from the resolved model. Nothing reads a generated compose file back as input.

## Sources of truth

There is no database. Everything is a file, and each kind of data has one home:

| Data | Lives in | Owned by |
|---|---|---|
| Experiment intent: services, apps, uses, TLS overrides | `experiment.yaml` in the project directory | User (committed; never holds secrets) |
| App code | Project directory | User |
| Install settings (domain, defaults) | `<BB_HOME>/config.yaml`, overridable by `BB_*` env vars | User, through `bb config` or by editing |
| Certificate authority | `<BB_HOME>/ca/` | Engine |
| Per-experiment generated state: secrets, leaf certificates, state metadata | `<project>/.bb/` | Engine |
| Runtime artifacts: compose files, mounted cert files, trust bundle | `<project>/.bb/runtime/` | Engine (regenerated on every `bb up`; disposable) |

## Install home (`BB_HOME`)

- Defaults to `os.UserConfigDir()/breadboard` (`~/Library/Application Support/breadboard` on macOS); overridable by the `BB_HOME` env var. Directory mode `0700`.
- `config.yaml`: install settings, loaded through bkit `settings` in layers: built-in defaults (`settings.Map`) → `config.yaml` (`settings.File` with the yaml driver) → `BB_*` env vars (`settings.Env`).
- `ca/root.crt` and `ca/root.key` (`0600`): the install's CA ([`security.md`](security.md)).

## Project state (`.bb/`)

Created by `bb init` next to `experiment.yaml`, directory mode `0700`.

```
.bb/
  .gitignore          contains "*": the whole directory is never committed, without editing the project's .gitignore
  state.json          format version, bb version that last wrote it, experiment id
  secrets.json        generated secrets (0600)
  certs/<service>/    leaf certificate and key per service (key 0600)
  runtime/            the generated compose project: docker-compose.yml, docker-compose.dev.yml,
                      ca.pem, trust-bundle.pem, tls/<service>/, config/<service>/
  lock                advisory file lock
```

- **Writes are atomic**: write a temp file in the same directory, `fsync`, rename.
- **Locking**: `bb up`, `bb down`, and other state-changing operations hold an exclusive advisory lock on `.bb/lock` (`flock`), so a CLI command and the running UI never change one experiment at the same time. A second caller gets a clear "busy" error naming the operation in progress.
- **Deleting `.bb/`** after `bb down --volumes` resets the experiment: the next `bb up` generates fresh secrets and certificates.

## Versioning and migrations

Every file Breadboard owns carries a format version, and every format only moves forward:

| File | Version field | Upgrade |
|---|---|---|
| `experiment.yaml` | `version` | `bb` reads every older version. `bb migrate` rewrites the file to the current version; `bb` never rewrites the user's file silently. |
| `.bb/state.json` and siblings | `formatVersion` | Upgraded in place on load, in order, one step per version. |
| `<BB_HOME>/config.yaml` | `version` | Upgraded in place on load. |

- `state.json` records the bb version that last wrote the project's state.
- A binary that meets a version newer than it knows refuses to write and tells the user to upgrade `bb`.
- Each upgrade step ships with a test that loads a fixture of the previous version.

## Runtime layout

- Compose project name: `bb-<experiment>`.
- `docker-compose.yml` is the whole stack: infra services and app services built from their `dev` stage with the spec's `command`. `docker-compose.dev.yml` adds the development loop: each app's source bind-mounted at `/src` (relative to the generated file) and named volumes for the Go module and build caches. `bb up` uses both files. The `prod` stage is for building release images by hand.
- App containers also get `BB_SERVICE` (their name). Their health check is that the main process is running, unless the spec's `health` opts into an HTTP check (then `BB_HEALTH_PORT` is set too).
- `bb up` waits up to 5 minutes (`--timeout`) for every container to be running and healthy, and fails fast when one exits, keeps restarting, or reports unhealthy.
- Labels on every service: `breadboard.experiment=<name>`, `breadboard.experiment-id=<id from state.json>`, `breadboard.service=<name>`, `breadboard.kind=infra|app`, and `breadboard.config-hash`, a hash of the service's mounted certificates and configuration files. A new hash changes the service's config, so `docker compose up` recreates it and reissued certificates take effect. Networks and volumes carry the experiment labels too. The engine finds its containers by label, never by name parsing.
- Compose projects are named after the experiment, so two directories with the same experiment name would share one project. `bb up` and `bb down` refuse to run when Docker holds containers, networks, or volumes labeled with the same name but another experiment id.
- Ports: nothing is published to the host by default. A port is published only when the spec asks for it, always bound to `127.0.0.1`.

## Web UI and MCP (Phases 3 and 4)

Not part of Phase 1. Both are wrappers over the core API above.

- `bb ui` serves on `127.0.0.1` from the same process as the engine, and opens the browser.
- Local web endpoints that can drive Docker need protecting even on localhost, because a malicious page can reach them through DNS rebinding or cross-site requests. `bb ui` therefore:
  - binds only to `127.0.0.1`;
  - generates a random token per launch, puts it in the URL it opens, and exchanges it for a session cookie (sessions held in bkit `stash` with the in-memory driver and a TTL);
  - rejects requests whose `Host` or `Origin` header is not the UI's own.
- `bb mcp` speaks MCP over stdio; MCP clients configure the command `bb mcp`.
- How the UI's explorers reach services on an experiment's private network is open question Q-11.

## CLI surface (Phase 1)

Each command is a thin wrapper over one core operation.

| Command | Effect |
|---|---|
| `bb init [name]` | Create `experiment.yaml` and `.bb/` in the current directory; create `BB_HOME` and the CA if missing. |
| `bb catalog` | List available infra service types. |
| `bb add <type> [--name n]` | Add an infra service to `experiment.yaml`. |
| `bb remove <name>` | Remove a service or app from `experiment.yaml`. |
| `bb starters` | List starters. |
| `bb starter <id> [--dir path]` | Apply a starter: write code, add app services. |
| `bb up [--timeout d]` | Reconcile state, materialize, `docker compose up -d`, wait for health. |
| `bb down [--volumes]` | Stop the experiment; `--volumes` also deletes its data. |
| `bb status` | Per service: state, health, CPU, memory; plus TLS warnings. |
| `bb logs [service] [-f] [--tail n]` | Stream logs. |
| `bb creds [service]` | Show generated credentials and connection strings. |
| `bb learn <type>` | Print the catalog entry's learning notes. |
| `bb ca [--pem]` | Show CA details and how to trust it on the host (never auto-installs); `--pem` prints the certificate. |
| `bb config [get [key] \| set <key> <value>]` | List, read, or change install settings. |
| `bb migrate` | Upgrade `experiment.yaml` to the current spec version. |
| `bb export compose [--out dir] [--secrets inline\|dotenv]` | Write a standalone compose stack (default `export/compose`). |
| `bb version` | Print the bb version. |

`bb add`, `bb remove`, and `bb starter` edit `experiment.yaml` through a YAML node tree, so the user's comments and ordering survive.

The CLI is built on the standard library `flag` package with a small subcommand dispatcher. Flags may follow positional arguments (`bb logs consumer -f`), and the global `-C dir` runs a command as if started in `dir`. Commands find the experiment by walking up from the current directory to the nearest `experiment.yaml`. Exit codes: 0 success, 1 error, 2 usage.

## Repository layout

```
bb/                     main package: `go install github.com/brian-nunez/breadboard/bb`; wires the core into the wrappers
internal/
  core/                 core API: one method per operation, request/result types, events, sentinel errors
  cli/                  CLI wrapper: subcommand dispatch, flag parsing, rendering
  web/                  web UI wrapper: templ components, handlers, embedded assets (Phase 3)
  mcp/                  MCP wrapper (Phase 4)
  spec/                 experiment.yaml types, parse, validate, version upgrades
  model/                resolved model and resolver
  catalog/              one file per catalog entry + embedded learning notes
  pki/                  CA and leaf issuance (crypto/x509, stdlib only)
  state/                BB_HOME and .bb/ files: atomic writes, locking, format upgrades
  config/               install settings via bkit settings
  secrets/              generation and materializers (inline, dotenv)
  compose/              compose materializer
  runtime/              docker compose invocation, health waiting, stats
  starters/             starter engine (embed.FS + text/template)
  export/               exporters (Phase 7+)
starters/                    package embedding the templates (starters.FS)
starters/go/<area>/<name>/   starter templates; every file ends in .tmpl so Go never compiles starter code as part of this module
docs/design/            this design
```

## Internal libraries (bkit)

Breadboard's own internals use Brian's [bkit](https://github.com/brian-nunez/bkit) modules wherever one fits. Starters never use bkit; they use the official client libraries ([`starters.md`](starters.md)).

| Need | bkit module | Use in Breadboard | Phase |
|---|---|---|---|
| Configuration | `settings` + `settings/drivers/yaml` | Layered install settings: defaults → `config.yaml` → `BB_*` env. | 1 |
| Rule checks | `gate` | Spec and policy validation: naming rules, TLS-off-requires-reason, reserved fields, phase-gated features. `gate.Join` reports every violation at once. | 1 |
| Key/value store | `stash` + `stash/drivers/memory` | UI sessions with TTL; short-lived caches such as container stats. | 3 |
| Relational database | `rdb` + `rdb/drivers/postgres` | Postgres explorer in the UI. | 3 |
| Object storage | `objex` + `objex/drivers/s3` | Bucket explorer for the S3 Compatible entry. | 3 |

`experiment.yaml` is parsed directly with `go.yaml.in/yaml/v3` (strict decoding to reject unknown keys, and node-level editing to preserve comments), which `settings` does not offer. Where bkit has no matching capability, the engine uses the standard library; any other direct dependency needs Brian's approval and a row in [`decisions.md`](decisions.md).

## Engineering conventions

- Module path `github.com/brian-nunez/breadboard`. Go toolchain pinned to `go1.27.4` in `go.mod`.
- Standard library first, then bkit; every direct dependency is listed in [`decisions.md`](decisions.md).
- `gofmt`/`goimports`; explicit errors with sentinel values and `errors.Is`/`errors.As`; no panic-driven flow.
- Tests: `go test ./...`; golden files for every materializer (regenerate with `-update`); Docker-backed tests behind the `integration` build tag.
