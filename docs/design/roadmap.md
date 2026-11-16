# Roadmap

Each phase needs Brian's approval before work starts. A phase is **done** only when every item in its completion criteria has been verified by actually running it.

## Phase 1: Core engine and CLI

**Focus:** the core engine. The CLI is the only wrapper in this phase, and it stays thin: every command parses flags, calls one core operation, and renders the result. No web UI, no MCP server.

**Outcome:** on a machine with Docker, this sequence ends with a Go producer and consumer exchanging messages over TLS through a real Kafka broker, without the user touching a credential:

```bash
go install github.com/brian-nunez/breadboard/bb@latest
bb init kafka-lab
bb add kafka
bb starter kafka/producer-consumer
bb up
bb logs consumer -f
```

**Kickoff (approved, D-030):**

1. Set `origin` to `git@github.com:brian-nunez/breadboard.git`.
2. Commit the design docs, `AGENTS.md`, and `CLAUDE.md` to `main` and push it, so the empty remote starts with the agreed design.
3. Build Phase 1 on a feature branch and open a pull request to `main` (GitHub Flow).

**Scope:**

- Core API (`internal/core`): every Phase 1 operation in [`architecture.md`](architecture.md#core-api), with request/result types, an event sink, and sentinel errors. Docker behind an internal interface.
- Spec: parse, strict validation on bkit `gate` (TLS-off-requires-reason, reserved fields rejected), comment-preserving edits, version upgrades.
- Resolved model: defaults, edges from `uses`, listeners, and segments (the plaintext segment logic exists and is tested, even though v1 starters stay TLS-only).
- File-based state: `BB_HOME` (`config.yaml` on bkit `settings`, CA) and project `.bb/` (secrets, certs, runtime), atomic writes, `flock` operation lock, format versions with forward upgrades, refusal of newer versions.
- PKI: name-constrained CA, leaf issuance with SPIFFE identity and both EKUs, reissue on expiry.
- Secrets: generation, `inline` and `dotenv` materializers.
- Catalog: `kafka`, `postgres`, `redis`, `consul`.
- Compose materializer; runtime over the `docker compose` CLI (up, down, status, logs, health waiting).
- Starter `kafka/producer-consumer`.
- Compose export.
- CLI wrapper for every core operation ([`architecture.md`](architecture.md#cli-surface-phase-1)).
- Distribution: `bb/` main package, installable with `go install github.com/brian-nunez/breadboard/bb@…`.

**Build order:** core API skeleton and sentinel errors → spec → model → state → PKI → Kafka entry with TLS (highest risk first) → compose materializer → runtime → starter → remaining entries → export → CLI wrapper.

**Completion criteria:**

- [x] `gofmt -l .` is empty; `go vet ./...` passes; `go test ./...` passes.
- [x] Every core operation is tested through the core API with a fake Docker runtime; no core test shells out or prints.
- [x] `internal/core` and everything below it contain no `fmt.Print*`, `os.Exit`, `os.Stdin`, or `flag` usage (checked by a test or lint rule).
- [x] Golden tests cover the compose materializer for every catalog entry, for both secret modes, and for a plaintext edge.
- [x] Unit tests prove: CA name constraints reject an out-of-domain leaf; leaf SANs, identity URI, and EKUs are correct; reissue happens below the 30-day threshold; validation reports every listed rule with its YAML path.
- [x] `Init` creates `.bb/` (`0700`, self-ignoring `.gitignore`), `secrets.json` and keys at `0600`, and `BB_HOME` with the CA when missing.
- [x] Tests prove: writes are atomic (no partial file after an interrupted write); a state file with a newer `formatVersion` is refused; a second holder of the operation lock gets `ErrBusy`; `AddService`/`RemoveService` preserve comments in `experiment.yaml`.
- [x] `go install` of the module path into an empty `GOPATH` produces a working `bb`, and `bb version` reports the module version. (Verified through a local module proxy serving the branch as `v0.1.0-phase1test`; repeat against the real tag at release.)
- [x] The rendered starter passes `go build ./...` and `go vet ./...`.
- [x] Integration test (`go test -tags integration ./internal/core/`, real Docker): `Up` all four entries → each reports healthy over TLS → producer sends N messages → consumer receives exactly N → `Down` with volumes leaves no containers, networks, or volumes labeled for the experiment.
- [x] The exported compose stack starts with plain `docker compose` and reaches healthy without `bb`.
- [x] Docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated.
- [x] Integration test for plaintext segments: a client on a plaintext edge reaches the Postgres, Kafka, and Consul plaintext listeners; a TLS-only client on the shared network cannot.

## Phase 2: Starters

`kafka/sink-to-postgres`, `redis/cache-invalidation`, `consul/service-discovery` (Consul client sidecar registers each app; apps resolve peers through the Consul API). Each with an integration test that proves its core behavior.

Catalog entry `s3-compatible` (SeaweedFS) with starter `s3/objects-basics` (buckets, keys, multipart upload, presigned URLs).

**Also built (Brian's direction or found while building):** `consul/self-registration` as a labeled alternative to the sidecar starter (D-055); `register` in `experiment.yaml` for the sidecar (D-053); unique starter app names (D-047); `TLS_CERT_FILE`, `TLS_KEY_FILE` and `BB_FQDN` for apps (D-048, D-054); a `2g` app memory default so `go run` can compile (D-050); `.bb/runtime` synced instead of rewritten, which fixed running containers losing their mounted files on a second `bb up` (D-052).

**Completion criteria:**

- [x] `gofmt -l .` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes, including rendering and building every starter.
- [x] `kafka/sink-to-postgres`: after the sink is killed mid-stream and restarted, every produced order is stored exactly once.
- [x] `redis/cache-invalidation`: after the last write, the reader serves every current price within a window shorter than the cache TTL.
- [x] `consul/service-discovery`: the caller reaches the greeter by name over verified TLS through the sidecar's registration; a stopped greeter drops out of the healthy instances.
- [x] `consul/self-registration`: the same lookup works; a stopped greeter deregisters itself on SIGTERM.
- [x] `s3-compatible`: configured buckets exist; S3 is HTTPS only; internal SeaweedFS ports are unreachable from other containers; gRPC rejects clients without a certificate and with another lab service's certificate.
- [x] `s3/objects-basics`: the delimiter listing shows one level, the multipart upload completes, and a presigned GET returns the exact bytes.
- [x] A second `bb up` leaves running, unchanged containers with their mounted files intact.
- [x] The full integration suite (`go test -tags integration ./internal/core/`), Phase 1 tests included, passes on the top of the Phase 2 stack.
- [x] Docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-047 to D-055).

## Phase 3: Web UI

Build to the accepted design in [`ui/`](ui/README.md) (D-031), served by `bb serve` (D-060).

templ + Tailwind (standalone CLI, no npm), components written in-repo (ADR-0013), assets embedded. Topology rendered as server-side SVG (plaintext edges dashed red); live status, CPU and memory, and logs over SSE; explorers for Kafka topics, groups and lag, Postgres tables and row counts, Redis keys, the Consul catalog, and S3 buckets, read through Docker (ADR-0012); docs pages per technology (D-061); starter pages. A wrapper over the core: anything the UI needs that the core lacks (explorer queries, the topology) becomes a core operation first. `bb` adds no client libraries (D-059).

**Also built (Brian's direction or found while building):** components written in-repo instead of templUI (ADR-0013); explorers read through Docker instead of management ports (ADR-0012); a read-only `ServiceDetails` that never returns secrets (D-065); explorer results loaded after the page because Kafka's tools take seconds (D-067); a home page and starter pages (D-068); search and a phone layout (D-069); the Consul server's unused TLS gRPC port turned off to match the design.

**Completion criteria:**

- [x] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift in the templ output or the compiled stylesheet.
- [x] `bb serve` listens only on a loopback address, requires the per-launch sign-in link (session cookie, `HttpOnly`, `SameSite=Strict`), rejects a foreign `Host` (421) or `Origin` (403), refuses open redirects, and sends a CSP with no inline scripts or styles (tested).
- [x] The overview, architecture, service pages and logs render against a real experiment and update live over SSE (checked in a browser against frames 03, 04, 05).
- [x] The UI never shows a secret: `ServiceDetails` returns secret variables empty, and no generated secret appears in any page (tested, and checked against a running lab).
- [x] Explorers for Kafka, Postgres, Redis, Consul and S3 Compatible return real data from the pinned images (integration tests), and a stopped service shows how to start it.
- [x] Docs for Getting started and all five catalog types: every technology page and guide renders and every "On this page" link has a heading (tested); facts were checked against official docs and library source.
- [x] The home page, starter list and every starter page render (tested; checked against frames 01, 09, 10).
- [x] Search finds docs, starters and services; on a 390px screen the dashboard has no horizontal overflow (checked in a browser against frame 12).
- [x] The full integration suite passes on the top of the Phase 3 stack.
- [x] Docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-059 to D-069, ADR-0012, ADR-0013).


## Phase 4: MCP server

Tools that mirror the CLI (create experiment, add service, apply starter, up/down, status, logs, creds, learn, export), served by `bb mcp` over stdio.

## Phase 5: Proxies and relays

Catalog entries: Traefik, Envoy, HAProxy, PgBouncer. Upstream edges, terminate/passthrough/re-encrypt modes, private segments for plaintext hops. Host access through `*.localhost` routing.

## Phase 6: mTLS and multiple CAs

Edge `tls.clientAuth`, identity-based authorization (Kafka ACLs, Postgres cert auth, Consul intentions), multiple CAs and rotation drills.

## Phase 7: Exporters

Kubernetes → Ansible → Terraform AWS → Terraform GCP → Terraform Cloudflare.

## Phase 8: More languages and services

TypeScript starters; further catalog entries.
