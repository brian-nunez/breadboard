# Architecture

## Components

**The core is the product.** Every capability lives in the core engine (`internal/core`). The CLI, the web UI, and the MCP server are **wrappers**: they translate their input into a core call and render the result. A front end holds no domain logic, so any capability the CLI has, the web UI and MCP server get by wrapping the same call.

One Go binary, `bb`, runs on the host and contains the core and all three wrappers. Breadboard itself never runs in a container: only the experiments do.

```
              ┌──────────────── bb (one binary, host) ────────────────┐
              │   CLI          Web UI (bb serve)     MCP (bb mcp)     │
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
| Web UI | `bb serve` | 3 | Local web server on `127.0.0.1`: topology, health, logs, explorers, docs. templ + Tailwind, embedded in the binary. Read-only. |
| MCP server | `bb mcp` | 4 | stdio MCP server exposing core operations as tools, on the official Go SDK (ADR-0014). |

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
| `AddService`, `RemoveService` | Edit `experiment.yaml`. Removing a service or app still listed in an app's `uses` or a proxy's `upstreams` is refused. |
| `Starters`, `ApplyStarter` | List starters; write starter code and update the spec. |
| `Up`, `Down` | Reconcile, materialize, run, wait for health; stop and optionally delete data. |
| `Status`, `Logs` | Per-service state, health, resources, TLS warnings; log stream. Each app instance row carries its app, instance number, replica count and sidecars, and is followed by its sidecars' rows (type, ports, env they give the app, transparent mode); the pod's namespace holder is never a row but decides the instance's state while it is down. Edges carry `Via` and `ToApp` for Envoy outbound targets (Phase 5). A proxy's row lists its upstream edges, each with its `Mode` (`on`, `off`, `passthrough`); every terminate hop is a plaintext warning (Phase 5). |
| `Creds`, `CA` | Generated credentials and connection strings; CA details. |
| `PostgresTables`, `RedisKeys`, `ConsulCatalog`, `KafkaTopics`, `S3Buckets` | Read-only explorers for the web UI: each runs the service's own CLI tool with `docker exec` and parses its output (ADR-0012) (Phase 3). |
| `EnvoyClusters`, `PgBouncerPools` | Read-only sidecar explorers (Phase 5): Envoy's admin API (`/clusters`, `/stats`, `/listeners` over bash `/dev/tcp`, host addresses mapped to replicas with `getent` inside the sidecar) and PgBouncer's `SHOW POOLS` / `SHOW STATS` through `psql` inside the sidecar, both with `docker exec` (ADR-0012). They take an app, an instance, or the sidecar's service name. |
| `HAProxyStats`, `TraefikRoutes`, `NginxStatus` | Read-only proxy explorers (Phase 5), each through `docker exec` against the proxy's loopback-only endpoint (ADR-0012): HAProxy's stats CSV (per backend and server: the replica at its address, status, last check, sessions, picks, bytes, errors), Traefik's API (routers, services, each server's UP/DOWN), NGINX `stub_status` (open source has no per-server counters) with what each upstream resolves to. Backends are matched to upstreams through `catalog.Proxy.Unit`. `EnvoyClusters` and `PgBouncerPools` also take an infra `envoy` or `pgbouncer` service. |
| `StarterDetails` | A starter's rendered files and apps without writing anything, for the web UI's starter pages (Phase 3). |
| `ServiceDetails` | Read-only view of one service for the web UI: listeners, who uses it (apps, and proxies with it as an upstream), the env apps receive (secret values never returned), its certificate (Phase 3). For an app: its instances and its sidecars (Phase 5); for a sidecar: what it is; for a proxy: its upstreams with mode, port, instances and route (Phase 5). |
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
  - UI components are written in `internal/web` with templ and Tailwind (ADR-0013); no component kit is copied in.
  - Starter templates and learning notes are embedded with `go:embed`.
- `bb version` reads the module version from `runtime/debug.ReadBuildInfo`, so `go install …@vX.Y.Z` reports the right version without build flags.
- Supported hosts for v1: macOS and Linux.

## Pipeline

1. **Load** `experiment.yaml` ([`spec.md`](spec.md)), the source of truth for the experiment. Validate with `gate` checks.
2. **Resolve** the spec against the catalog into the resolved model: defaults applied, edges derived from `uses` (and from Envoy sidecars' outbound targets and proxies' `upstreams`), listeners and segments computed, TLS policy evaluated ([`security.md`](security.md)), app replicas expanded into instances, sidecars resolved against the sidecar catalog.
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
| Runtime artifacts: compose files, mounted cert files, trust bundle | `<project>/.bb/runtime/` | Engine (synced on every `bb up`: unchanged files are left in place because running containers bind-mount them; disposable) |

## Install home (`BB_HOME`)

- Defaults to `os.UserConfigDir()/breadboard` (`~/Library/Application Support/breadboard` on macOS); overridable by the `BB_HOME` env var. Directory mode `0700`.
- `config.yaml`: install settings, loaded through bkit `settings` in layers: built-in defaults (`settings.Map`) → `config.yaml` (`settings.File` with the yaml driver) → `BB_*` env vars (`settings.Env`).
- `ca/root.crt` and `ca/root.key` (`0600`): the install's CA ([`security.md`](security.md)).
- `ca/lock`: every project shares the CA, so project locks cannot protect it. Loading, creating, or replacing the CA holds an exclusive `flock` on this file; a second caller waits (up to 30 seconds) instead of failing, then sees the CA the first one saved. A key that does not match its certificate is treated as missing and replaced, and leaves signed by the old CA are reissued on the next `bb up`.

## Project state (`.bb/`)

Created by `bb init` next to `experiment.yaml`, directory mode `0700`.

```
.bb/
  .gitignore          contains "*": the whole directory is never committed, without editing the project's .gitignore
  state.json          format version, bb version that last wrote it, experiment id
  secrets.json        generated secrets (0600)
  certs/<service>/    leaf certificate and key per service (key 0600)
  runtime/            the generated compose project: docker-compose.yml, ca.pem,
                      trust-bundle.pem, tls/<service>/, config/<service>/
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
- `docker-compose.yml` is the whole stack: infra services and app services built from their `dev` stage with the spec's `command`. Apps are compiled while the image builds and run as their container's main process; after an edit, `bb up` rebuilds the image (incrementally, through a BuildKit cache mount) and recreates the container. There is no source mount or development overlay. The `prod` stage is for building release images by hand.
- App containers also get `BB_SERVICE` (their name), `BB_FQDN` (their address), `TLS_CERT_FILE` and `TLS_KEY_FILE`. Their health check is that the main process is running, unless the spec's `health` opts into an HTTP check (then `BB_HEALTH_PORT` is set too).
- `bb up` waits up to 5 minutes (`--timeout`) for every container to be running and healthy, then requires all of them to stay healthy for a 5-second settle window. It fails fast when one exits, is restarting, reports unhealthy, or restarted at any point during the wait (its restart count rose or its start time changed), so a program that crashes right after its first health check is reported, not hidden.
- Labels on every service: `breadboard.experiment=<name>`, `breadboard.experiment-id=<id from state.json>`, `breadboard.service=<name>`, `breadboard.kind=infra|app|sidecar|pod|init`, and `breadboard.config-hash`, a hash of the service's mounted certificates and configuration files. A new hash changes the service's config, so `docker compose up` recreates it and reissued certificates take effect. Networks and volumes carry the experiment labels too. The engine finds its containers by label, never by name parsing.
- Compose projects are named after the experiment, so two directories with the same experiment name would share one project. `bb up` and `bb down` refuse to run when Docker holds containers, networks, or volumes labeled with the same name but another experiment id.
- Ports: nothing is published to the host by default. A port is published only when the spec asks for it, always bound to `127.0.0.1`.
- Replicas: an app with `replicas: N` renders services `<app>-1` … `<app>-N` (ADR-0016), each with the aliases `<app>-<n>.<domain>` and `<app>.<domain>`.
- Proxies (Phase 5): a proxy is an infra service with no data volume and no `depends_on` on its upstreams; it finds them through Docker's DNS at runtime. Its generated config (or the user's `file`, copied in at each render) is under `config/<proxy>/`. An app behind a terminating proxy runs as a pod: its holder joins `plain-<app>` with a static address per replica (the segment's `ip_range` is the upper half), and `<instance>-netinit` installs the plaintext guard (D-121). An existing network whose addressing changed (an app segment without its `ip_range`, like the default network's subnet) is recreated by `bb up`: containers stop, volumes stay.

### Pods

An app with sidecars becomes a pod (ADR-0015), and so does the target of a terminating proxy (D-121). For each instance the compose file has:

| Service | Role |
|---|---|
| `<app>-pod` | Namespace holder (`alpine:3.24.4`, `sleep infinity` with `init: true`, health check `true`, `16m`). Owns the networks, aliases and published ports that the app container owns outside a pod. Label `breadboard.kind=pod`. |
| `<app>-netinit` | Transparent mode, or the plaintext guard of a terminate target: one-shot init container built from a generated Dockerfile (`build/<app>-netinit/` in the bundle), the only holder of `NET_ADMIN`, `restart: "no"`. Label `breadboard.kind=init`. |
| `<app>-<sidecar>` | Each sidecar, from its type's `Render` hook, with the app's certificate. |
| `<app>` | The app, name unchanged, so `bb logs <app>` keeps working. |

All but the holder use `network_mode: service:<app>-pod`. Start order is pod (healthy) → netinit (`service_completed_successfully`) → sidecars (healthy) → app, through `depends_on`. The app and every sidecar mount the named volume `<app>-shared` at `/var/run/bb` (`BB_SHARED_DIR`). With replicas each instance is its own pod (`<app>-<n>-pod`, `<app>-<n>`, `<app>-<n>-envoy`, …). An app with only `register` is not a pod: its agent `<app>-agent` keeps `network_mode: service:<app>`, so its compose output is unchanged.

The holder is an implementation detail: `bb status`, the web UI and MCP list the app and its sidecars, never the holder, but the holder's state counts toward the app's: while it is missing, stopped or unhealthy, the app reports that state, and a holder restart fails `bb up` like an app restart.

## Web UI and MCP (Phases 3 and 4)

Not part of Phase 1. Both are wrappers over the core API above.

- `bb serve` serves on a loopback address (default `127.0.0.1:7777`; other addresses are refused) from the same process as the engine, and opens the browser. Generated files (`*_templ.go`, `static/app.css`) are rebuilt with `scripts/web.sh generate`, which runs templ at the `go.mod` version and Tailwind's standalone CLI pinned by SHA-256; `scripts/web.sh check` fails on drift.
- Local web endpoints that can drive Docker need protecting even on localhost, because a malicious page can reach them through DNS rebinding or cross-site requests. `bb serve` therefore:
  - binds only to `127.0.0.1`;
  - generates a random token per launch, puts it in the URL it opens, and exchanges it for an `HttpOnly`, `SameSite=Strict` session cookie (sessions held in bkit `stash` with the in-memory driver and a 12-hour TTL), then redirects so the token leaves the address bar;
  - rejects requests whose `Host` or `Origin` header is not the UI's own;
  - sends a strict content security policy (no inline scripts or styles; resource bars are SVG attributes) and forbids framing.
- `bb mcp` speaks MCP over stdio with the official Go SDK (ADR-0014), serving 2026-07-28 clients and older ones that still send `initialize`. MCP clients configure the command `bb mcp`; stdout carries only protocol messages.
- Tools mirror the CLI and add the read-only operations agents need (D-072). Inputs and outputs are MCP-side view types with `snake_case` names whose JSON Schemas the SDK derives; results are structured content plus the same JSON as text.

  | Kind | Tools |
  |---|---|
  | About bb | `guide` (embedded agent guide), `version`, `catalog`, `learn`, `starters`, `starter_details` (every file's content), `ca`, `config_get` |
  | Inspect | `status`, `logs` (no follow; default 200, at most 2,000 lines per service), `creds` (with values, D-071), `service_details`, `postgres_tables`, `redis_keys`, `consul_catalog`, `kafka_topics`, `s3_buckets`, `envoy_clusters`, `pgbouncer_pools` (sidecars and infra services), `haproxy_stats`, `traefik_routes`, `nginx_status` |
  | Change | `init`, `add_service`, `apply_starter`, `migrate`, `config_set`, `export_compose`, `up` |
  | Destructive | `remove_service`, `down` (with `volumes`, deletes data) |

- Safety (D-073): `dir` and `export_compose`'s `out` resolve against the directory `bb mcp` started in (or `-C`), following symlinks, and anything outside it is refused. `--read-only` registers only the tools that change nothing the user owns. Annotations mark reads `readOnlyHint`, `down` and `remove_service` `destructiveHint`, and every tool `openWorldHint: false`.
- Progress: `up`, `down`, `apply_starter` and `export_compose` send each core event as a progress notification when the request carries a progress token, and return the events in the result (on failure, in the error text). Cancelling a call cancels its context, so the core operation stops and releases the experiment's lock.
- Explorers read service data through Docker: each runs the service's own command-line tool with `docker exec`, inside the experiment's network ([ADR-0012](../adr/README.md)). Status comes from `docker inspect` and `docker stats`. The UI never changes the system.

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
| `bb serve [--addr host:port] [--no-open]` | Serve the read-only web UI on a loopback address (default `127.0.0.1:7777`) and open the browser with a one-time sign-in link (Phase 3). |
| `bb mcp [--read-only]` | Serve the core as MCP tools over stdio for AI agents; `--read-only` exposes only tools that change nothing the user owns (Phase 4). |
| `bb version` | Print the bb version. |

`bb add`, `bb remove`, and `bb starter` edit `experiment.yaml` through a YAML node tree, so the user's comments and ordering survive.

The CLI is built on the standard library `flag` package with a small subcommand dispatcher. Flags may follow positional arguments (`bb logs consumer -f`), and the global `-C dir` runs a command as if started in `dir`. Commands find the experiment by walking up from the current directory to the nearest `experiment.yaml`. Exit codes: 0 success, 1 error, 2 usage.

## Repository layout

```
bb/                     main package: `go install github.com/brian-nunez/breadboard/bb`; wires the core into the wrappers
internal/
  core/                 core API: one method per operation, request/result types, events, sentinel errors
  cli/                  CLI wrapper: subcommand dispatch, flag parsing, rendering
  web/                  web UI wrapper: templ components, handlers, embedded assets (Phase 3);
                        styles/input.css is the Tailwind source, static/ is embedded
  mcp/                  MCP wrapper: tools over the official Go SDK, embedded agent guide (Phase 4)
  spec/                 experiment.yaml types, parse, validate, version upgrades
  model/                resolved model and resolver
  catalog/              one file per catalog entry + embedded learning notes
    sidecar/            sidecar catalog: types that run in an app's pod (Phase 5)
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

`experiment.yaml` is parsed directly with `go.yaml.in/yaml/v3` (strict decoding to reject unknown keys, and node-level editing to preserve comments), which `settings` does not offer. `bb mcp` uses the official MCP Go SDK (ADR-0014). Where bkit has no matching capability, the engine uses the standard library; any other direct dependency needs Brian's approval and a row in [`decisions.md`](decisions.md).

## Engineering conventions

- Module path `github.com/brian-nunez/breadboard`. Go toolchain pinned to `go1.27.4` in `go.mod`.
- Standard library first, then bkit; every direct dependency is listed in [`decisions.md`](decisions.md).
- `gofmt`/`goimports`; explicit errors with sentinel values and `errors.Is`/`errors.As`; no panic-driven flow.
- Tests: `go test ./...`; golden files for every materializer (regenerate with `-update`); Docker-backed tests behind the `integration` build tag.
