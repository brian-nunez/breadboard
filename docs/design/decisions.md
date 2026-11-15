# Decisions

Decisions that are costly to reverse have a full record in [`docs/adr/`](../adr/README.md); the rows below marked → ADR-NNNN link to it. New decisions of that kind get an ADR first and a row here; smaller decisions are recorded here only.

Brian owns material decisions: system boundaries, data models and file formats, public contracts (CLI, spec, env contracts), security policy, and direct dependencies. Record each new decision here when it is made; move an open question into the log once Brian resolves it. A superseded decision stays in the log, marked with what replaced it.

## Decision log

| Id | Date | Decision |
|---|---|---|
| D-001 | 2026-11-14 | Purpose: local lab for real system design practice. Real services only; no cloud emulators or mocks. → [ADR-0003](../adr/README.md) |
| D-002 | 2026-11-14 | No Breadboard SDK or wrappers. Users and starters use official client libraries. → [ADR-0006](../adr/README.md) |
| D-003 | 2026-11-14 | Every experiment runs in Docker Compose: infra and user app code, each with limits, health checks, explicit env vars, healthy-dependency ordering. → [ADR-0003](../adr/README.md) |
| D-004 | 2026-11-14 | Engine, CLI, UI, and MCP server are written in Go. → [ADR-0001](../adr/README.md) |
| D-005 | 2026-11-14 | Web UI uses templ + templUI + Tailwind. → [ADR-0008](../adr/README.md) |
| D-006 | 2026-11-14 | Name: Breadboard; CLI `bb`. |
| D-007 | 2026-11-14 | Starters start with Go, targeting Go 1.27.4. The engine also pins toolchain go1.27.4. |
| D-008 | 2026-11-14 | Default starter libraries: `franz-go`, `pgx/v5`, `go-redis/v9`, `hashicorp/consul/api`. Users may install anything else. → [ADR-0006](../adr/README.md) |
| D-009 | 2026-11-14 | Engine YAML dependency: `go.yaml.in/yaml/v3`. → [ADR-0011](../adr/README.md) (carried forward from ADR-0007) |
| D-010 | 2026-11-14 | TLS on by default for every container-to-container hop. Plaintext requires an explicit, reasoned choice (supports proxy/relay termination practice). → [ADR-0005](../adr/README.md) |
| D-011 | 2026-11-14 | Breadboard generates a local CA; all containers trust it. Services are addressed as `<service>.<domain>` via Docker network aliases. → [ADR-0005](../adr/README.md) |
| D-012 | 2026-11-14 | Domain defaults to `bb.internal` and is configurable. → [ADR-0005](../adr/README.md) |
| D-013 | 2026-11-14 | One CA per install in v1; multiple CAs later for testing. Design must allow it. → [ADR-0005](../adr/README.md) |
| D-014 | 2026-11-14 | mTLS is a later phase; v1 must be extensible toward it (both EKUs, identities, reserved policy fields). → [ADR-0005](../adr/README.md) |
| D-015 | 2026-11-14 | Generated secrets are materialized inline in compose or into a `.env`; exporters transform them into their target's medium. Storage location: see D-026. |
| D-016 | 2026-11-14 | ~~CLI on the host; UI and MCP in containers.~~ Superseded by D-024. |
| D-017 | 2026-11-14 | Exporter targets: Kubernetes, Ansible, Terraform (AWS, GCP, Cloudflare). Azure and Stripe dropped. → [ADR-0009](../adr/README.md) |
| D-018 | 2026-11-14 | Breadboard internals use Brian's bkit modules where they fit: `settings`, `gate` now; `stash`, `rdb`, `objex` in the UI phase. Mapping in [`architecture.md`](architecture.md#internal-libraries-bkit). → [ADR-0011](../adr/README.md) (carried forward from ADR-0007) |
| D-019 | 2026-11-14 | Module path and repository: `github.com/brian-nunez/breadboard` (`git@github.com:brian-nunez/breadboard.git`). |
| D-020 | 2026-11-14 | ~~SQLite state DB schema.~~ Superseded by D-025. |
| D-021 | 2026-11-14 | ~~SQLite state DB as source of truth, mounted into containers.~~ Superseded by D-025. |
| D-022 | 2026-11-14 | ~~SQLite driver choice.~~ Superseded by D-025. |
| D-023 | 2026-11-14 | Object storage is the catalog entry **S3 Compatible** (`s3-compatible`), implemented with SeaweedFS, never branded as AWS S3. → [ADR-0010](../adr/README.md) |
| D-024 | 2026-11-14 | `bb` never runs in a container. CLI, web UI, and MCP server are one Go binary on the host, installed with `go install`. Only experiments run in containers. → [ADR-0001](../adr/README.md) |
| D-025 | 2026-11-14 | No SQLite. `experiment.yaml` is the source of truth for an experiment. Breadboard state is files with format versions and forward-only upgrades; the bb version that last wrote state is recorded. → [ADR-0004](../adr/README.md) |
| D-026 | 2026-11-14 | State locations: install settings and the CA in `BB_HOME`; per-experiment secrets, certificates, and runtime files in the project's self-ignoring `.bb/` directory (layout in [`architecture.md`](architecture.md#project-state-bb)). Confirmed by Brian (former Q-10). → [ADR-0004](../adr/README.md) |
| D-027 | 2026-11-14 | Main package at `bb/`; install with `go install github.com/brian-nunez/breadboard/bb@latest`. → [ADR-0001](../adr/README.md) |
| D-028 | 2026-11-14 | Core-first architecture: all capability lives in the core engine API; the CLI, web UI, and MCP server are thin wrappers over it. Phase 1 is the core plus the CLI only. → [ADR-0002](../adr/README.md) |
| D-029 | 2026-11-14 | S3 starter library: `aws-sdk-go-v2`. Starter libraries default to the most popular SDK in the ecosystem. → [ADR-0006](../adr/README.md) |
| D-030 | 2026-11-14 | At Phase 1 kickoff: set `origin` to `git@github.com:brian-nunez/breadboard.git`, commit the design docs to `main`, push; build Phase 1 on a feature branch with a pull request. (Former Q-03.) |
| D-031 | 2026-11-14 | Web UI design v1 accepted: read-only, calm, minimal copy, no alerts; commands instead of action buttons; Kafka overview and starter page are the templates for all tech and starter pages. Design in [`ui/`](ui/README.md). → [ADR-0008](../adr/README.md) |
| D-032 | 2026-11-15 | Phase 1 image pins: `apache/kafka:4.3.2`, `postgres:18.7-alpine3.24`, `redis:8.10.3-alpine3.23`, `hashicorp/consul:1.22.7`; starters build on `golang:1.27.4-alpine3.24` and `alpine:3.24.4`. The catalog's Go code stays the source of truth for versions. |
| D-033 | 2026-11-15 | Plaintext segments: an `internal` compose network `plain-<target>` with a `/24` from `10.213.0.0/16` (skipping subnets Docker already uses), recorded in `.bb/state.json`; the target sits at `.10` with alias `<target>.plain.<domain>`. Entries bind their plaintext listener to that address or limit it to that subnet. |
| D-034 | 2026-11-15 | Redis has no plaintext listener in v1, because Redis cannot bind one port to a segment-only address; validation rejects `tls.mode: off` toward it. Plaintext Redis comes with terminating proxies (Phase 5). |
| D-035 | 2026-11-15 | App runtime conventions: `docker-compose.yml` runs apps from their `dev` stage; `docker-compose.dev.yml` bind-mounts source at `/src` and adds Go cache volumes; apps default to `512m`/`0.5` cpus. ~~Default `/healthz` check on port 8080.~~ Health default superseded by D-042. |
| D-036 | 2026-11-15 | `kafka/producer-consumer` uses `github.com/twmb/franz-go v1.22.3` plus its admin module `github.com/twmb/franz-go/pkg/kadm v1.20.0` (part of the franz-go project under D-008). Starter template files all end in `.tmpl` so the engine module never compiles them. |
| D-037 | 2026-11-15 | Generated passwords are 32 base32 characters (`crypto/rand.Text`, 160 bits): safe in URLs, shells, and compose files without escaping. Env variables from a second service of the same type are prefixed with that service's name. Dotenv secret variables are `<SERVICE>__<KEY>` (double underscore), so names never collide. |
| D-038 | 2026-11-15 | The `domain` setting must end in a special-use suffix (`.internal`, `.test`, `.localhost`, `.home.arpa`), so the name-constrained CA can never sign for a real site. |
| D-039 | 2026-11-15 | Every Docker resource carries `breadboard.experiment-id`. `bb up`/`bb down` refuse to act when resources labeled with the same experiment name belong to another id (another directory), and subnet allocation treats only the experiment's own networks as reusable. |
| D-040 | 2026-11-15 | Each service carries `breadboard.config-hash` over its mounted certificates and configuration, so compose recreates the container when they change (for example, a reissued leaf). |
| D-041 | 2026-11-15 | Consul: server RPC requires TLS client certificates, LAN gossip is encrypted with a generated key, and DNS, gRPC, and WAN gossip are disabled, so no unencrypted port is reachable outside a private segment. |
| D-042 | 2026-11-15 | Starters are the bare minimum: one `main.go` per app using the official library directly, with no helper packages or health code. App containers get a default health check that the main process is running; an HTTP check is opt-in with `health` in `experiment.yaml`. (Brian's direction.) |
| D-043 | 2026-11-15 | Where bkit has no matching module, internals use the standard library; any other direct dependency of `bb`, including starter client libraries, needs Brian's approval and its own row here. Supersedes the client-library fallback in ADR-0007. (Brian's direction.) → [ADR-0011](../adr/README.md) |
| D-044 | 2026-11-15 | The install CA has its own lock, `BB_HOME/ca/lock`, held from loading the CA until any replacement is saved; callers wait for it rather than fail. A CA key that does not match its certificate is replaced. |
| D-045 | 2026-11-15 | `bb down` also removes networks, and with `--volumes` volumes, labeled with the experiment's id, so resources of services or segments removed since an earlier `bb up` do not outlive the experiment. Matching the id, not the name, leaves a same-named experiment in another directory alone. |
| D-046 | 2026-11-15 | The shared Go cache volumes mount with `volume.nocopy`, so Docker never copies the image's module cache into them. Apps start together, and concurrent copies into one new volume failed with `mkdir ...: file exists`. Go fills the caches itself on first start. |
| D-047 | 2026-11-15 | Starter app names are unique across all starters. Applying a starter whose app name is already used, other than by the same starter in the same directory, fails with `ErrExists` before anything is written, instead of skipping the app. |
| D-048 | 2026-11-15 | Every app gets `TLS_CERT_FILE` and `TLS_KEY_FILE` pointing at its own certificate and key, so apps that serve TLS keep certificate paths out of code. |

## Open questions

| Id | Question | Recommendation |
|---|---|---|
| Q-11 | How the web UI's explorers (Phase 3) reach services on an experiment's private network, given `bb` runs on the host. (a) `docker exec` each service's own CLI tools; (b) publish a management port per infra service on a random `127.0.0.1` port and connect with TLS, verifying the certificate against the FQDN; (c) a short-lived helper container on the network. | (b). Out of scope for Phase 1; decide before Phase 3. |
