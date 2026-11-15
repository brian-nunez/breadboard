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

- [ ] `gofmt -l .` is empty; `go vet ./...` passes; `go test ./...` passes.
- [ ] Every core operation is tested through the core API with a fake Docker runtime; no core test shells out or prints.
- [ ] `internal/core` and everything below it contain no `fmt.Print*`, `os.Exit`, `os.Stdin`, or `flag` usage (checked by a test or lint rule).
- [ ] Golden tests cover the compose materializer for every catalog entry, for both secret modes, and for a plaintext edge.
- [ ] Unit tests prove: CA name constraints reject an out-of-domain leaf; leaf SANs, identity URI, and EKUs are correct; reissue happens below the 30-day threshold; validation reports every listed rule with its YAML path.
- [ ] `Init` creates `.bb/` (`0700`, self-ignoring `.gitignore`), `secrets.json` and keys at `0600`, and `BB_HOME` with the CA when missing.
- [ ] Tests prove: writes are atomic (no partial file after an interrupted write); a state file with a newer `formatVersion` is refused; a second holder of the operation lock gets `ErrBusy`; `AddService`/`RemoveService` preserve comments in `experiment.yaml`.
- [ ] `go install` of the module path into an empty `GOPATH` produces a working `bb`, and `bb version` reports the module version.
- [ ] The rendered starter passes `go build ./...` and `go vet ./...`.
- [ ] Integration test (`-tags integration`, real Docker): `Up` all four entries → each reports healthy over TLS → producer sends N messages → consumer receives exactly N → `Down` with volumes leaves no containers, networks, or volumes labeled for the experiment.
- [ ] The exported compose stack starts with plain `docker compose` and reaches healthy without `bb`.
- [ ] Docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated.

## Phase 2: Starters

`kafka/sink-to-postgres`, `redis/cache-invalidation`, `consul/service-discovery` (Consul client sidecar registers each app; apps resolve peers through the Consul API). Each with an integration test that proves its core behavior.

Catalog entry `s3-compatible` (SeaweedFS) with starter `s3/objects-basics` (buckets, keys, multipart upload, presigned URLs).

## Phase 3: Web UI

Build to the accepted design in [`ui/`](ui/README.md) (D-031).

templ + templUI + Tailwind (standalone CLI, no npm). Topology rendered as server-side SVG (plaintext edges dashed red); live status, CPU and memory, and logs over SSE; explorers for Kafka topics and lag, Postgres tables and row counts, Redis keys, the Consul catalog; learning notes. Served by `bb ui` from the host binary with assets embedded. A wrapper over the core: anything the UI needs that the core lacks (explorer queries, event streams) becomes a core operation first. Resolve Q-11 (explorer connectivity) before starting.

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
