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

Tools that mirror the CLI (create experiment, add service, apply starter, up/down, status, logs, creds, learn, export), served by `bb mcp` over stdio, on the official MCP Go SDK (ADR-0014).

**Also built (Brian's direction):** `creds` returns credential values so agents can debug connections (D-071); read-only tools beyond the CLI: `service_details`, `starter_details`, the five explorers, and an embedded `guide` (D-072); directory confinement and `--read-only` (D-073).

**Completion criteria:**

- [x] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift.
- [x] `bb mcp` answers both a 2026-07-28 client (`server/discover`, per-request version) and a legacy client (`initialize`) over stdio, and writes nothing to stdout but protocol messages (tested; checked against the built binary).
- [x] Every tool in D-072 is listed with a description, derived input and output schemas, and annotations; `--read-only` drops exactly the tools that change experiment.yaml, project files, settings or containers (tested).
- [x] Tools refuse a `dir` or `out` outside the root, including through a symlink (tested).
- [x] `up`, `down`, `apply_starter` and `export_compose` send progress notifications and return their events; cancelling `up` stops it and releases the lock (tested).
- [x] An agent flow over stdio against real Docker passes: `init` → `apply_starter` → `up` → `redis_keys` → `creds` → `logs` → `down` with volumes, leaving no labelled Docker resources (integration test).
- [x] The full integration suite passes on the top of the Phase 4 stack.
- [x] Docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-070 to D-073, ADR-0014).

## Phase 5: Proxies and relays

Delivered in five waves, each approved by Brian before it starts.

### Wave A: Sidecar foundation

Real sidecars that share an app's network namespace, so the app uses them over `127.0.0.1`, and replicas for later load-balancing examples (ADR-0015, ADR-0016, D-074 to D-102).

**Scope:** `apps.<name>.sidecars` with a sidecar catalog (`envoy`: inbound TLS termination, outbound TLS origination with app-to-app edges, opt-in transparent mode; `pgbouncer`; `fluent-bit`; `register` moved onto the internal `consul-agent` type); the pod model (namespace holder, start order, shared `/var/run/bb`); `apps.<name>.replicas` as named instances with per-instance certificate SANs; status, UI and MCP showing each app with its sidecars and replicas; an Envoy explorer read through `docker exec` as a core operation; the architecture graph drawing sidecars under their app and app-to-app edges.

**Completion criteria:**

- [x] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift.
- [x] Golden tests: register-only experiments render output identical to `main`; new fixtures for a pod with each sidecar type, replicas 3, and transparent mode.
- [x] Integration (real Docker): Envoy inbound makes a plaintext app reachable only through TLS from another container.
- [x] Integration: Envoy outbound across 3 replicas reaches more than one instance.
- [x] Integration: in transparent mode the app calls `http://<target>.<domain>:<port>` and the target sees TLS; with `egress: deny` an undeclared destination is refused; stopping Envoy blocks the app (no bypass).
- [x] Integration: PgBouncer: the app connects to `127.0.0.1:6432` and Postgres sees connections from PgBouncer over TLS.
- [x] Integration: Fluent Bit: a line written to `/var/run/bb/logs/app.log` appears as an object in the bucket.
- [x] Integration: `register` (Consul) behaviour is unchanged.
- [x] Six sidecar starters (`sidecar/envoy-ambassador`, `envoy-tls-termination`, `envoy-transparent`, `circuit-breaking`, `pgbouncer-pool`, `log-shipper`) each pass an integration test that asserts the behaviour they teach; the Sidecars docs section renders in `bb serve`.
- [x] Every integration test leaves no labelled containers, networks or volumes; the full suite (`go test -tags integration ./internal/core/`) passes on the top of the wave A stack.
- [x] Docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-074 onward, ADR-0015, ADR-0016).

### Wave B: Proxies as infra

Reverse proxies, load balancers and a connection pooler as infra services in front of apps and other services, with a TLS mode per upstream: re-encrypt, terminate and passthrough ([`security.md`](security.md#proxy-and-relay-modes-phase-5), D-103 onward).

**Scope:** `services.<name>.upstreams` and `services.<name>.file` ([`spec.md`](spec.md#proxies-and-upstreams)); upstream edges from a proxy to apps (every replica) and infra, with plaintext segments for terminate hops (`plain-<app>`, `BB_PLAIN_LISTEN_ADDR`); the proxy rendering seam in the catalog (resolved upstreams, the 8443/9443 convention, the `<NAME>_URL` / `<NAME>_PASSTHROUGH_ADDR` contract, `file` copied into the bundle); infra entries `haproxy` (`haproxy:3.4.7`), `envoy` (`envoyproxy/envoy:v1.40.2`), `traefik` (`traefik:v3.7.19`, file provider only), `nginx` (`nginx:1.30.6-alpine`) and `pgbouncer` (`edoburu/pgbouncer:v1.26.1-p0`, one `postgres` upstream, the Postgres env contract with `verify-full`); upstream edges in status, the architecture graph and service details, labelled by mode; explorers (HAProxy stats, Traefik routes, NGINX `stub_status`; `EnvoyClusters` and `PgBouncerPools` for the infra Envoy and PgBouncer), with web UI tabs and MCP tools `haproxy_stats`, `traefik_routes`, `nginx_status`; learning notes for every entry.

**Completion criteria:**

- [x] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift.
- [x] Golden tests: experiments without proxies render byte-identical to `main`; a golden fixture per proxy type covers every mode the type supports.
- [x] Unit tests check each generated config with its pinned image: `haproxy -c`, `envoy --mode validate`, `nginx -t`; Traefik's file config is loaded by a running container in an integration test.
- [x] Integration (real Docker), for each HTTP proxy (HAProxy, Envoy, Traefik, NGINX): re-encrypt to an app with 3 replicas reaches more than one replica, and the client verified the proxy's certificate.
- [x] Integration, for each HTTP proxy: terminate makes the app's plaintext port reachable from the proxy and from no other container.
- [x] Integration: passthrough (at least HAProxy and Envoy) shows the client the backend's own certificate.
- [x] Integration: PgBouncer infra: two apps share the pool, and Postgres sees connections only from PgBouncer, over TLS.
- [x] Integration: each explorer returns real data (`TestIntegrationProxyExplorers`: HAProxy, Traefik, NGINX, infra Envoy and infra PgBouncer).
- [x] Every integration test leaves no labelled containers, networks or volumes; the full suite (`go test -tags integration ./internal/core/`) passes on the top of the wave B stack.
- [x] Docs in `docs/design/` match what was built: catalog entries, spec fields, security modes, architecture operations and MCP tools; [`decisions.md`](decisions.md) updated.

### Wave C: Load-balancing and reverse-proxy examples

Starters that balance across replicas and route by host and path, with health checks, ejection and retries to break and observe (D-136 onward).

**Scope:** starters that bring proxies (`starters.Starter.Services`, written by `bb starter` through the YAML node tree; D-136 to D-139); templates that name the lab domain and the experiment's infra services (D-161); thirteen starters, each with a commented native proxy config the learner owns, a README with exercises, a web narrative and an integration test of its lesson: `lb/round-robin` (NGINX), `lb/least-connections` (HAProxy), `lb/power-of-two-choices` (Traefik), `lb/consistent-hashing` (Envoy), `lb/sticky-sessions` (HAProxy), `lb/weighted-canary` (Traefik), `lb/blue-green` (NGINX), `lb/health-checks` (HAProxy and Envoy), `proxy/l4-vs-l7`, `proxy/tls-modes` and `proxy/connection-draining` (HAProxy), `proxy/request-mirroring` (Envoy), `pgbouncer/pooling-modes` (PgBouncer); web docs sections for HAProxy, Envoy, Traefik, NGINX and PgBouncer and the "Load balancing" concept guides (D-157 to D-160); Envoy DNS clusters on the `envoy.clusters.dns` extension (D-162).

**Completion criteria:**

- [x] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift after commit.
- [x] Every starter renders and applies; its native config passes the image's own check (`haproxy -c`, `envoy --mode validate` with no deprecation warning, `nginx -t`, Traefik loading its routers).
- [x] Integration (real Docker): each starter's test asserts its lesson: round robin gives 3 replicas about a third each; under `leastconn` the slow replica gets fewer requests; under `p2c` every replica serves and the slow one gets fewer; with consistent hashing a key always lands on the same replica and removing one remaps only a minority of keys; a sticky client keeps one replica and moves when it stops; the canary's v2 share is within tolerance of 10%; blue-green shows only blue, then only green, with no errors across the switch; health checks remove a failing replica and restore it; L7 routes by path or header while L4 balances per connection; the client sees the proxy's certificate for re-encrypt and terminate and the backend's for passthrough, and the terminate backend is unreachable from elsewhere; mirroring sends copies to v2 while the client sees only v1; draining a replica under load fails no request; PgBouncer keeps server connections within `pool_size` under many clients and logs a session-state trap.
- [x] Every integration test leaves no labelled containers, networks or volumes; the full suite (`go test -tags integration ./internal/core/`) passes on the top of the wave C stack.
- [x] Docs pages render (HTTP 200 through `bb serve`), are in search, link only registered starters and existing docs pages; facts match the implementation and the official docs for the pinned versions; every starter's guide link resolves.
- [x] README starters table and AGENTS stage updated; docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-136 to D-162).

### Wave D: API-gateway examples

Starters for gateway patterns: routing, request transformation, versioning, rate limiting, resilience, authentication at the edge, aggregation and gRPC (D-163 onward).

**Scope:** ten starters in the `gw` area, each with a commented native gateway config the learner owns, a README with exercises, a web narrative and an integration test of its lesson: `gw/path-host-routing`, `gw/request-transformation`, `gw/forward-auth` and `gw/bff-aggregation` (Traefik), `gw/api-versioning` (NGINX), `gw/rate-limiting` (Envoy and NGINX), `gw/circuit-breaking`, `gw/retries-and-timeouts`, `gw/jwt-auth` and `gw/grpc-routing` (Envoy); no private key, token or password committed (the JWT issuer and the auth service generate theirs at runtime); an "API gateways" docs section with seven concept guides, each starter listed in the section of the proxy it runs on (D-176 to D-178).

**Completion criteria:**

- [x] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift after commit.
- [x] Every starter renders and applies; its native config passes the image's own check (`envoy --mode validate`, `nginx -t`, Traefik loading its routers), for Envoy v1.40, Traefik v3.7 and NGINX 1.30.
- [x] Integration (real Docker): each starter's test asserts its lesson: each path and Host route reaches its own service and an unknown path gets the default; the backend sees the added and rewritten headers and path and never a removed header; v1 and v2 answer from their own backends and v1 answers carry the deprecation headers; under a burst, each rate limiter's accepted and rejected counts match its configuration within tolerance; under overload, circuit-breaker overflow answers are immediate 503s and the backend's concurrency never exceeds the threshold; retries raise a flaky backend's success rate, and a retry budget bounds backend load under failure; a missing or forged JWT gets 401 and a valid one 200 with its claims forwarded; forward auth answers 401 for an unauthorised request and the backend sees the identity header for an authorised one; one BFF call returns merged data and both services logged a request; two gRPC services are reached by name through one listener and a method timeout returns `DEADLINE_EXCEEDED`.
- [x] Every integration test leaves no labelled containers, networks or volumes; the full suite (`go test -tags integration ./internal/core/`) passes on the top of the wave D stack.
- [x] Docs pages render (HTTP 200 through `bb serve`), are in search, link only registered starters and existing docs pages; facts match the implementation and the official docs for the pinned versions; every starter's guide link resolves to its `/docs/api-gateways` guide.
- [x] README starters table and AGENTS stage updated; docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-163 to D-179).

### Wave E: `*.localhost` host access

Reaching services from the host by name through `*.localhost` routing, including apps with replicas, which cannot publish ports.

**Scope:** a host-only CA, `host`, name-constrained to `.localhost` (D-185); the spec field `host` on apps and HTTPS infra services (D-187); a per-lab host router `bb-host` on Traefik, published on a stable `127.0.0.1` port, terminating with a host certificate and re-encrypting to every replica against the lab CA (D-186, D-188 to D-190); host URLs and the host CA in `bb up`, `bb status`, `bb ca`, the web UI and MCP (D-191); compose export (D-192); trust and resolution guidance (D-193); a "Host access" docs page in the web UI. Decided in [ADR-0017](../adr/README.md).

**Completion criteria:**

- [ ] `gofmt -l` is empty; `go vet ./...` passes (with and without the `integration` tag); `go test ./...` passes; `scripts/web.sh check` reports no drift after commit.
- [ ] Unit tests: the host CA rejects a name outside `.localhost` and any URI SAN; the host certificate's SANs, EKU and reissue on a changed name set; `root` and every lab certificate are unchanged by host access; `host` validation reports every rule with its YAML path; port allocation (start, wrap, taken port, kept port).
- [ ] Golden tests: experiments without `host` render byte-identical to `main`; a golden fixture covers the host router for an app with replicas, an app with one instance and an infra proxy; Traefik loads the generated config with every router and no error.
- [ ] Integration (real Docker, `TestIntegrationHostAccess`): an app with 3 replicas and an HAProxy proxy in front of it, both with `host`; from the host, with the host CA as the only root and SNI set to each name: the app's name reaches more than one replica, each instance name reaches only that instance, the proxy's name answers through HAProxy, an unknown name gets 404, and the presented certificate does not verify against the lab CA.
- [ ] Every integration test leaves no labelled containers, networks or volumes; the full suite (`go test -tags integration ./internal/core/ ./internal/mcp/`) passes on the top of the wave E stack in under 10 minutes.
- [ ] `bb ca` shows both CAs with trust steps; `bb status`, the web UI and MCP show the host URLs; the docs page renders (HTTP 200 through `bb serve`) and is in search.
- [ ] README and AGENTS stage updated; docs in `docs/design/` match what was built; [`decisions.md`](decisions.md) updated (D-185 onward).

## Phase 6: mTLS and multiple CAs

Edge `tls.clientAuth`, identity-based authorization (Kafka ACLs, Postgres cert auth, Consul intentions), multiple CAs and rotation drills.

## Phase 7: Exporters

Kubernetes → Ansible → Terraform AWS → Terraform GCP → Terraform Cloudflare.

## Phase 8: More languages and services

TypeScript starters; further catalog entries.
