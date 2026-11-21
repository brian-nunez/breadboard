# Starters

A starter takes the user from intent to running code: it writes a small, working project into the experiment's directory, adds the app services to `experiment.yaml`, and adds any infra services it needs that are missing. After that the code belongs to the user.

## Rules

- **Official client libraries only.** Starters import the real libraries and use them as their docs show. Breadboard ships no SDK or wrapper.
- **Bare minimum.** A starter is the least code that works and shows the concept: each app is one `main.go` that uses the library directly. No helper packages, no Breadboard plumbing, no code the lesson does not need. Comments explain concepts, not syntax.
- **Most popular wins. When choosing a starter's library, pick the one most of the ecosystem uses, so what the user learns transfers to real projects.
- **Configuration from env only.** Code reads the catalog env contract (`KAFKA_BROKERS`, `DATABASE_URL`, …). No Breadboard-specific config files.
- **Runs without Breadboard.** The project builds and runs anywhere the env vars and a trust bundle exist.
- **Containerised.** Each starter has a multi-stage `Dockerfile`:
  - `dev` stage: `golang:<exact>-alpine`, dependencies downloaded, then every app compiled with `go build -o /usr/local/bin/ ./cmd/...` while the image builds (a BuildKit cache mount keeps compiled packages between builds). Each app's `command` is its binary's name, so the program is the container's main process: it receives SIGTERM, and it is running, not compiling, when its health check passes. After editing code, `bb up` rebuilds the image and recreates the container.
  - `prod` stage: copies one binary from `dev` into a minimal non-root image, with labels `maintainer`, `version`, `description`, `platform`.
- **No health code.** Breadboard gives every app container a health check that its main process is running, so starters contain no health endpoints. An app that wants a readiness check opts in with `health` in `experiment.yaml`.
- **TLS through the environment.** Clients use the library's default TLS configuration, which verifies against `SSL_CERT_FILE`; no certificate paths appear in code.
- **Comments teach the why.** Explain concepts (consumer groups, offsets, at-least-once delivery, why invalidation follows the write), not the syntax.
- **Every starter README has two sections:** *Try this* (guided experiments) and *Break this* (failure drills: kill the broker, slow the consumer, exhaust memory).
- **Idempotent apply.** Re-applying a starter never overwrites user-modified files; it reports conflicts instead.
- **Unique app and service names.** App names, and the names of the proxy services a starter brings, are unique across every starter and never a catalog type's name (tests enforce it), so several starters can share one experiment. Re-applying a starter to the same directory leaves its existing apps and proxies alone; if a name it needs belongs to anything else in `experiment.yaml`, `bb starter` fails before writing any file.

## Language and libraries

- v1 starters are **Go**, targeting Go **1.27.4** (`go 1.27.4` in each starter's `go.mod`).
- Default libraries (users may add or swap anything):

| Service | Library |
|---|---|
| Kafka | `github.com/twmb/franz-go` |
| Postgres | `github.com/jackc/pgx/v5` |
| Redis | `github.com/redis/go-redis/v9` |
| Consul | `github.com/hashicorp/consul/api` |
| S3 Compatible | `github.com/aws/aws-sdk-go-v2/service/s3` |

## Shape

A starter is one Go module; each app is a `cmd/<app>` entry, and each app service in the spec sets `command` to run its entry. Templates live in `starters/go/<area>/<name>/`, are embedded in the binary, and render with `text/template`. Every template file ends in `.tmpl` (including `go.mod` and `go.sum`), so the Go tool never treats starter code as part of Breadboard's module.

A starter declares the catalog types it needs; applying it adds any missing one (named after its type) and points each app's `uses` at the experiment's service of that type. The default target directory is the starter's name (`./producer-consumer`); `bb starter <id> --dir <path>` overrides it.

`kafka/producer-consumer` pins `github.com/twmb/franz-go v1.22.3` and its admin client module `github.com/twmb/franz-go/pkg/kadm v1.20.0`.

`kafka/sink-to-postgres` pins the same franz-go modules and `github.com/jackc/pgx/v5 v5.13.0`. Its apps are `sink-producer` (JSON order events on `order-events`) and `sink` (creates the `orders` table, inserts with `ON CONFLICT DO NOTHING`, and commits offsets only after the write).

`redis/cache-invalidation` pins `github.com/redis/go-redis/v9 v9.23.0` with the same pgx and franz-go modules. Its apps are `cache-writer` (updates Postgres, then announces on `product-updates`), `cache-reader` (cache-aside with a 30-second TTL) and `cache-invalidator` (deletes the changed key).

`s3/objects-basics` pins `github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10` and `github.com/aws/aws-sdk-go-v2/config v1.33.18`. Its one app, `objects`, runs the walkthrough once (bucket, prefix listing, multipart upload, presigned GET) and then waits so its logs stay readable. Path-style addressing is the only S3 setting in code.

`consul/service-discovery` pins `github.com/hashicorp/consul/api v1.35.0`. Its `greeter` serves HTTPS on 8443 with no Consul code; `register: {port: 8443}` in its spec entry makes Breadboard add the `greeter-agent` sidecar (see `catalog.md`). `caller` resolves healthy `greeter` instances through the Consul API and calls one. A starter app declares `register` through `starters.App.Register`, which `bb starter` writes into `experiment.yaml`.

`consul/self-registration` is the labeled alternative, with the same Consul API pin. `self-greeter` registers itself (address from `BB_FQDN`, a TLS check, `DeregisterCriticalServiceAfter` as a crash safety net) and deregisters on SIGTERM; like every starter app it runs its built binary as the main process, which is what lets it receive SIGTERM. Its README points to the recommended starter first.

### Sidecar starters

The `sidecar/*` starters teach the patterns of Phase 5's sidecars (see `catalog.md`, "Sidecar catalog"). A starter app declares replicas and sidecars through `starters.App.Replicas` and `starters.App.Sidecars`; a sidecar's reference to an infra service names its catalog type and resolves to the experiment's service of that type, like `uses`. Apps that must name another app derive the lab domain from `BB_FQDN` (everything after the first dot). No starter app contains TLS code: inbound Envoy sidecars terminate TLS, and calls between apps go through Envoy.

- `sidecar/envoy-ambassador` has no library pins (standard library only). `ambassador-backend` runs 3 replicas behind inbound Envoy sidecars and answers with its instance name; `ambassador-caller` calls `AMBASSADOR_BACKEND_URL` (its outbound sidecar, `lb: round_robin`, `timeout: 2s`, `retries: 2`) and logs a tally.
- `sidecar/envoy-tls-termination` has no library pins. `plain-web` serves plain HTTP on `BB_LISTEN_ADDR` behind an inbound Envoy sidecar; `tls-client` calls it over verified TLS and shows that the app's port is refused from another container.
- `sidecar/envoy-transparent` has no library pins. `transparent-caller` has a transparent Envoy sidecar with `egress: deny` and one declared outbound target; it calls `transparent-backend` (declared, TLS on the wire) and `transparent-stranger` (undeclared: the caller's sidecar closes the connection, counted in `egress_denied.rbac.denied`) by name in plain HTTP, and `transparent-backend`'s address with an undeclared `Host` (answered 403 by the sidecar's HTTP route).
- `sidecar/circuit-breaking` has no library pins. `breaker-backend` runs 3 replicas; `POST /fail` and `POST /recover` on a replica's loopback toggle 503s. `breaker-caller`'s outbound sets `retries: 0` and `outlier: {consecutive_5xx: 3, ejection: 30s, max_ejection_percent: 50, interval: 5s}`. Envoy's default `max_ejection_percent` (10%) would block ejecting one host of three; the README has the learner set it back to 10 to watch ejections overflow, and owning the generated config (`envoy.file`) is an optional exercise.
- `sidecar/pgbouncer-pool` pins `github.com/jackc/pgx/v5 v5.13.0`. `pool-client` opens 50 client connections (`pgxpool`) through a PgBouncer sidecar in transaction mode with `pool_size: 10`, and logs how many server connections Postgres sees.
- `sidecar/log-shipper` pins `github.com/aws/aws-sdk-go-v2 v1.48.0`, `github.com/aws/aws-sdk-go-v2/config v1.33.18` and `github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10`. `log-writer` writes JSON lines with `log/slog` to `BB_LOG_DIR` and has no S3 code; its Fluent Bit sidecar ships them to the `app-logs` bucket; `log-reader` lists the bucket with the AWS SDK.

### Starters that bring proxies

A starter can add proxy services (Phase 5 wave C) through `starters.Starter.Services`. Each `starters.Service` has:

- `Name`: unique across starters, like app names, and not a catalog type.
- `Type`: a proxy type (`haproxy`, `envoy`, `traefik`, `nginx`, `pgbouncer`).
- `Upstreams`: `spec.Upstream` values. `Service` names an app of the starter, or a catalog type in `Requires`, which resolves to the experiment's service of that type like `uses`. `Port`, `Name` and `TLS` (`Mode`, `Reason`) are written as given, and validated like any upstream ([`spec.md`](spec.md#proxies-and-upstreams)).
- `File`: optional. The path of a rendered template (without `.tmpl`) relative to the starter's directory. It is written with the code, and the service's `file` is set to it relative to `experiment.yaml` (`./<dir>/<File>`), so the proxy runs the learner's native config instead of a generated one. Rendering fails when the template has no such file.
- `StaticFile`: optional, like `File`, for the service's `static_file` (Traefik only): a rendered template merged into the generated `traefik.yml`, such as `metrics` or `tracing` sections (D-206, D-207). Rendering fails when the template has no such file.
- `Config`: optional. The service's `config`, written as given (keys sorted) and validated against the catalog entry's options like any `config`, before any file is written (D-206).

An app of the starter reaches a proxy by listing the proxy's name in `starters.App.Uses` (`uses: [<proxy>]`); other entries of `Uses` stay catalog types.

`bb starter` writes the proxies through the YAML node tree (comments survive), validates the whole `experiment.yaml` before writing any file, and re-applies idempotently: an existing service of that name is the starter's own when it has the same type and either the same `file` or `static_file`, an upstream to one of the starter's apps in the same directory, or a client among those apps that uses it (D-137); anything else is `ErrExists`. An existing service is left as the learner has it, `config` included.

Templates render with `{{.Experiment}}` (the experiment name) and `{{.Domain}}` (the install's service domain, default `bb.internal`), so native configs can name their backends: `<app>.{{.Domain}}` resolves to every replica, `<app>-<n>.{{.Domain}}` to one. For each catalog type in `Requires`, `{{.Service "<type>"}}` is the experiment's service of that type (an existing one, or the one `bb starter` adds) and `{{.Config "<type>" "<key>"}}` one of its config options with catalog defaults applied, so a config can follow a Postgres that is not named `postgres` (D-161). A proxy config must keep what the catalog entry says its file must keep (the loopback health endpoint, Envoy's admin API, PgBouncer's auth files; see `catalog.md`), and contains no secrets.

`bb serve`'s starter page, `starter_details` (MCP) and `StarterDetails` (core) list each proxy, its upstreams, its `config` and its config files (`file` and `static_file`).

### Load-balancing strategy starters

`lb/round-robin`, `lb/least-connections`, `lb/power-of-two-choices` and `lb/consistent-hashing` each bring one proxy (`starters.Starter.Services`) with a commented native config the learner owns, and have no library pins (standard library only). They share one shape:

- A backend app with replicas that serves TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE` (the proxy re-encrypts to port 8443) and answers each request with its instance name (`BB_SERVICE`). Its latency knob is a constant at the top of its `main.go` and, at runtime, `POST /delay?d=<duration>` on a plain listener bound to `127.0.0.1:8080`, reached with `docker exec … wget`: no other container, the proxy included, can reach it.
- A client app that uses the proxy (`<PROXY>_URL`), trusts the lab CA through `SSL_CERT_FILE`, generates traffic and logs a running tally every 5 seconds: requests per replica with their share, status codes, p50 and p99.
- Where a strategy is compared with another, the proxy runs both side by side over the same replicas on two path prefixes, and the client drives both at once, so one log window shows the difference. The starter's single upstream carries the route name of the primary strategy's unit (`be_leastconn`, Traefik service `p2c`, Envoy cluster `ring-hash`), so the explorers tag it; the comparison unit is the file's own.

| Starter | Proxy | Backend | Client | What the config shows |
|---|---|---|---|---|
| `lb/round-robin` | `rr-edge` (NGINX) | `rr-backend` ×3, no delay | `rr-client`: 10 requests a second, one at a time | One `upstream` with `server rr-backend.<domain>:8443 resolve`, default weighted round robin, one worker; scaling the backend adds peers through DNS without restarting NGINX. |
| `lb/least-connections` | `lc-edge` (HAProxy) | `lc-backend` ×3, 50 ms, `lc-backend-3` 500 ms | `lc-client`: 6 requests in flight per strategy | `be_leastconn` (`balance leastconn`, default route) and `be_roundrobin` (`/roundrobin/`), each a `server-template` of the same replicas. |
| `lb/power-of-two-choices` | `p2c-edge` (Traefik) | `p2c-backend` ×3, 50 ms, `p2c-backend-3` 500 ms | `p2c-client`: 6 requests in flight per strategy | Services `p2c` (`strategy: p2c`, default route) and `wrr` (`/wrr/`), each listing the replicas by name, with health checks. |
| `lb/consistent-hashing` | `ch-edge` (Envoy) | `ch-backend` ×4, per-replica key cache (a miss costs 20 ms) | `ch-client`: the same 200 keys in `X-User-ID` every 5 seconds per policy; logs the stable placement, every key that moves, and moves since the baseline | Clusters `ring-hash` (`RING_HASH`, default route) and `maglev` (`MAGLEV`, `/maglev/`), both `STRICT_DNS` with a `hash_policy` on `x-user-id`. |
### Load-balancing pattern starters: stickiness, canary, blue-green, health checks

Each brings its proxies with a commented native config (`File`), backends that serve TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE` and name the instance that answered, and a client that logs a running tally. Every hop is re-encrypted and verified against the lab CA. None has library pins (standard library only). Decisions D-145 to D-149.

- `lb/sticky-sessions` (HAProxy `sticky-edge`): `sticky-backend` (3 replicas) keeps a visit count per `session` cookie in memory; `sticky-client` plays three users with their own cookie jars and one anonymous caller. `haproxy.cfg` lists one `server` per replica with `cookie s1`…`s3`, `cookie SERVERID insert indirect nocache httponly secure`, `option redispatch`, `retries 2` and `timeout connect 1s`. A stopped replica's users move with `visits=1`.
- `lb/weighted-canary` (Traefik `canary-edge`): `canary-v1` (2 replicas) and `canary-v2` (1); `dynamic.yml` routes everything to a `weighted` service (90/10, `healthCheck: {}`), plus a `canary-testers` router on ``Header(`X-Canary`, `always`)`` to v2. `canary-client` logs each version's share and errors (`X-App-Version` names the version on errors).
- `lb/blue-green` (NGINX `bg-edge`): `bg-blue` (v1) and `bg-green` (v2); `nginx.conf` proxies `/` to `https://$bg_live` (an upstream per colour), defaults `set $bg_live bg-blue;` and then includes `/etc/nginx/bg-live/*.conf` from the container's writable layer. `switch.sh <colour>` smoke-tests the colour through `/bg-<colour>/`, writes the switch file, runs `nginx -t` and `nginx -s reload` through `docker exec`. `bg-client` sends 20 requests a second and logs each cutover and every error.
- `lb/health-checks` (HAProxy `hc-active`, Envoy `hc-passive`): `hc-backend` (3 replicas) with a loopback `POST /fail` and `/recover` that stamps the state change time in every answer; `hc-active` uses `option httpchk` with `inter 2s fall 3 rise 2` and no passive checks, `hc-passive` uses `outlier_detection` (`consecutive_5xx: 3`, `interval: 1s`, `base_ejection_time: 10s`, `max_ejection_percent: 50`) and no active checks. `hc-client` sends the same traffic through both and logs per proxy the time to eject and to recover.

### Service discovery with a load balancer

`lb/consul-traefik` (D-226) has no library pins (standard library only) and requires `consul`. `ct-backend` runs 3 replicas that serve TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE`, answer with their instance name (`BB_SERVICE`) and contain no Consul or Traefik code: its spec entry has ``register: {port: 8443, tags: [traefik.enable=true, traefik.http.routers.ct-backend.rule=PathPrefix(`/`), traefik.http.services.ct-backend.loadbalancer.server.scheme=https]}``, so each replica's agent registers it with those tags. `ct-edge` is a Traefik service with `config: {consul_catalog: consul}`, no upstreams and no `file`: Breadboard's generated config is all it needs (D-225). `ct-client` uses `ct-edge`, calls it once a second and logs the running tally every 5 seconds like the other load-balancing clients. The README's drills scale to 5 replicas (Traefik is not recreated), stop a replica (dropped within about 10 seconds; it returns after `docker start` and a restart of its agent, whose network namespace was the stopped container's) and stop Consul (Traefik keeps its last routes). The key file on its page is `cmd/ct-backend/main.go`, whose header shows the `register` entry and what each tag does.

### Proxying starters (Phase 5 wave C)

Each brings one proxy with a native, commented config the learner owns (`File`), and backends that serve TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE` unless the lesson is a terminate hop. None has library pins except `pgbouncer/pooling-modes`.

- `proxy/l4-vs-l7` (HAProxy, `l4l7-edge`, `haproxy.cfg`): `l4l7-api` and `l4l7-web`, two replicas each, answer with their instance in `X-Instance`. The config's `frontend l7` (8443, `mode http`) routes `/api/` or `X-Route: api` to `be_api` and the rest to `be_web`; `frontend l4` (7443, `mode tcp`, TLS offload, no ALPN) sends every connection to `be_l4`, which lists both apps' replicas. `l4l7-client` derives the layer 4 address from `L4L7_EDGE_URL`'s host and logs, per round, the instances reached over one layer 7 connection and over four layer 4 connections, with a misrouted count.
- `proxy/tls-modes` (HAProxy, `tlsm-edge`, `haproxy.cfg`): upstreams `reencrypt` (`tlsm-reencrypt`, TLS), `terminate` (`tlsm-terminate`, `tls.mode: off` with a reason; it listens on `BB_PLAIN_LISTEN_ADDR` and has no TLS code) and `passthrough` (`tlsm-passthrough`, `tls.mode: passthrough`). `tlsm-client` logs the certificate names, issuer and fingerprint it verified on each, dialling `TLSM_EDGE_PASSTHROUGH_ADDR` with the backend's name as SNI for passthrough, and tries the terminate port directly.
- `proxy/request-mirroring` (Envoy, `mirror-edge`, `envoy.yaml`): the default route sends every request to cluster `v1` with a `request_mirror_policies` entry for `v2` at 100%; `/v1/` and `/v2/` reach each version unmirrored. `mirror-v2` takes 250 ms and fails one order in three and logs the copies it received and the shadow `Host`; `mirror-client` logs which version answered, failures and the slowest answer every 5 seconds.
- `proxy/connection-draining` (HAProxy, `drain-edge`, `haproxy.cfg`): `drain-backend` runs 3 replicas listed one `server` line each (so the runtime API can name them, `be_app/drain-backend-<n>`), with `option httpchk` on `GET /healthz` every second. On SIGTERM a replica answers `/healthz` with 503 for 4 seconds while serving, then `http.Server.Shutdown`. The runtime API is `stats socket ipv4@127.0.0.1:9999 level admin`. `drain-client` keeps eight requests in flight and logs per-replica counts and failures every 2 seconds.
- `pgbouncer/pooling-modes` (PgBouncer, `pool-central`, `pgbouncer.ini`) pins `github.com/jackc/pgx/v5 v5.13.0` and requires `postgres`. The config offers the Postgres database `app` twice in `[databases]`: `app` (`pool_mode=transaction pool_size=5`) and `app_session` (`pool_mode=session pool_size=8`). `pool-modes` runs 30 transaction and 6 session clients (one `pgx.Conn` each) that report `pg_backend_pid()`, and every 5 seconds runs a `set_config(…, false)` read-back trap, a SQL `PREPARE`/`EXECUTE` trap and a protocol-level prepared statement in both pools.

### API gateway starters (Phase 5 wave D)

Each teaches one thing an API gateway does beyond load balancing, on the gateway that shows it best. The narratives link the matching `/docs/api-gateways` guide (D-176).

#### Routing and transformation

Each brings one gateway with a native, commented config the learner owns (`File`), backends that serve TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE` and name themselves in `X-Instance`, and a client that sends the same round of requests every 5 seconds, logs what each backend saw, and counts what went wrong. Every hop is re-encrypted and verified against the lab CA.

- `gw/path-host-routing` (Traefik, `route-edge`, `dynamic.yml`), D-163: `route-users` (mounted at `/users`, prefix stripped), `route-orders` (owns `/orders/`, forwarded unchanged), `route-admin` (the virtual host `admin.route-edge.<domain>`, any path) and `route-default` (``PathPrefix(`/`)`` at priority 1, answers 404). `route-client` sends seven requests per round, two with `Host: admin.route-edge.<domain>` over the gateway's own TLS name, and `/usersettings` to prove the prefix rule matches segments; it logs each as `MISROUTED` when another service or status answers.
- `gw/request-transformation` (Traefik, `xform-edge`, `dynamic.yml`), D-164: `xform-backend` echoes the path and the headers the gateway touches. Routers `api` (`gateway-headers`, `strip-api`), `legacy` (`gateway-headers`, `replacePathRegex`) and `v0` (`redirectRegex`, permanent). `xform-client` sends a forged `X-User-Id` and `X-Forwarded-For` and its own `X-Request-Id`, follows the redirect by hand, and logs every promised transformation that did not happen as `UNEXPECTED`.
- `gw/api-versioning` (NGINX, `ver-edge`, `nginx.conf`), D-165: `ver-v1` and `ver-v2` serve `/orders/{id}` in two incompatible shapes. `/v1/` and `/v2/` choose by path, `Api-Version` by header on every other path (none → v1, unknown → 400), and server-level `add_header ... always` with map-driven values put `Deprecation`, `Sunset` and `Link` on v1 answers only. `ver-client` asks six ways per round and checks the version, the body and the deprecation headers.

#### Resilience

Each brings Envoy (and, for rate limiting, NGINX too) with a native, commented config the learner owns (`File`); backends serve TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE`, and every hop is re-encrypted and verified against the lab CA. None has library pins (standard library only). Decisions D-167 to D-170.

- `gw/rate-limiting` (Envoy `rate-token`, `envoy.yaml`; NGINX `rate-leaky`, `nginx.conf`): both limit per `X-Client-Id` to an average of 5 r/s with a burst of 10. `rate-token` uses `local_ratelimit` with filter-level `rate_limits` (a `request_headers` action) and a wildcard descriptor (`entries: [{key: client}]`, `token_bucket: {max_tokens: 10, tokens_per_fill: 5, fill_interval: 1s}`, `max_dynamic_descriptors: 1000`, `always_consume_default_token_bucket: false`); `rate-leaky` uses `limit_req_zone $http_x_client_id … rate=5r/s` and `limit_req … burst=10` (no `nodelay`) with `limit_req_status 429` and `$limit_req_status` in a response header. `rate-client` sends bursts of 30 for alice and bob every 10 seconds and a steady 4 r/s for carol through both; `rate-backend` logs what arrived per gateway and client.
- `gw/circuit-breaking` (Envoy `cb-edge`, `envoy.yaml`): cluster `cb-backend` over HTTP/1.1 with `circuit_breakers` `max_connections: 5`, `max_pending_requests: 5`, `max_requests: 5`, `max_retries: 3`, `track_remaining: true`; route timeout 5s, no retries. `cb-backend` takes 200 ms plus 50 ms per other request in flight and logs its peak concurrency; `cb-client` runs 20 closed-loop workers and logs served and overflow (503 with `x-envoy-overloaded`) counts and latencies.
- `gw/retries-and-timeouts` (Envoy `retry-edge`, `envoy.yaml`): routes `/flaky/no-retries` (timeout 1s) and `/flaky/retries` (timeout 2s, `retry_on: 5xx`, `num_retries: 3`, `per_try_timeout: 0.25s`) to `retry-flaky` (25% 503, 15% stuck 3s); `/down/no-budget` and `/down/budget` with the same retry policy to `retry-down` (always 503 after 50 ms) through clusters with `max_retries: 1000` and with `retry_budget: {budget_percent: 20, min_retry_concurrency: 1}`. The virtual host sets `include_request_attempt_count` and `include_attempt_count_in_response`. `retry-client` sends a fixed rate per route and logs success, attempts per request and p99; `retry-down` logs attempts per client request per route.

#### Identity, composition and gRPC

Each brings one gateway with a native, commented config the learner owns (`File`), apps that serve TLS on 8443 with `TLS_CERT_FILE`/`TLS_KEY_FILE`, and a client that logs what the gateway did with each request. Every hop is TLS verified against the lab CA, including the gateway's calls to an issuer or auth service. No secret is committed: keys and session tokens are generated at runtime. Decisions D-171 to D-175.

- `gw/jwt-auth` (Envoy `jwt-edge`, `envoy.yaml`) pins `github.com/golang-jwt/jwt/v5 v5.3.1`. `jwt-issuer` generates an ES256 key at startup, serves its JWKS and signs 2-minute tokens for `alice` and `bob` on `POST /token`; `jwt_authn` fetches the JWKS through the TLS-verified `issuer` cluster, requires a token on `/api/`, forwards `sub` and `scope` as `X-Jwt-Sub`/`X-Jwt-Scope` and strips the token. `jwt-client` calls with no token, a forged signature, a token of alice's issued for `reports-api` (the issuer takes `aud`, default `jwt-api`; Envoy answers 403), alice's token, and bob's token plus a spoofed `X-Jwt-Sub`; `jwt-backend` logs the claims it received.
- `gw/forward-auth` (Traefik `fauth-edge`, `dynamic.yml`): `forwardAuth` to `fauth-auth`'s unrouted `/verify` over TLS (`tls.ca`), sending only `Authorization`, copying back `X-Auth-User`/`X-Auth-Roles`, then a `headers` middleware drops the token. `fauth-auth` mints in-memory opaque sessions at `/auth/login`, revokes them at `/auth/logout`, and refuses admin paths without the role (403). `fauth-client` covers no token, a made-up token, alice, bob with a spoofed `X-Auth-User`, bob on `/api/admin/`, and bob after logout.
- `gw/bff-aggregation` (Traefik `bff-edge`, `dynamic.yml`): routes `/profile/`, `/orders/` and `/bff/`. `bff-api` fans out to `bff-profile` (80 ms) and `bff-orders` (120 ms) concurrently with a 300 ms per-call timeout and returns only the home screen's fields; `bff-client` loads the screen both ways with a simulated 150 ms mobile round trip per call and logs calls, time and bytes. Every app logs the client's `X-Request-Id`.
- `gw/grpc-routing` (Envoy `grpc-edge`, `envoy.yaml`) pins `google.golang.org/grpc v1.85.0` and `google.golang.org/protobuf v1.36.12`. `grpc-catalog` (2 replicas, `catalog.v1.Catalog`: `GetItem`, `Search` at 800 ms) and `grpc-inventory` (`inventory.v1.Inventory/CheckStock`) register hand-written `grpc.ServiceDesc`s over well-known types (contracts in `proto/`). Routes by path, `Search` first with `max_stream_duration`/`grpc_timeout_header_max: 0.5s`; HTTP/2 clusters with ALPN `h2` and gRPC health checks; `grpc_stats` per method. `grpc-client` uses one connection and logs the replicas reached, the inventory answer, Search's `DeadlineExceeded` and an unrouted service's `Unimplemented`.

## Your own app (`bb add app`)

Starters are lessons. `bb add app <name> [--uses a,b] [--dir path]` starts the learner's own program instead: the least code that runs in the lab and shows what the lab gives it. It is not a starter (no `Starter` entry, no README drills), but it follows the starter rules above: official clients at the starters' versions, configuration from env only, a multi-stage `Dockerfile`, and no Breadboard code.

What it does (core `AddApp`; CLI `bb add app`; MCP `add_app`):

- **Name.** A DNS label not yet used in the shared namespace (`bb-host` is reserved); it is also the binary and the `command`, and the Go module is `example.com/<name>`, so an app named `log` or `context` does not shadow a standard library package. `app` is a reserved word after `bb add`, and no catalog type may take it (tested).
- **Uses.** By default every infra service in `experiment.yaml`, in spec order (the "installed software"). `--uses a,b` picks some, each an existing infra service, listed once; `--uses ''` picks none.
- **Directory.** `--dir`, relative to `experiment.yaml`, default `./<name>`: inside the project and outside `.bb`, checked lexically and again on the canonical destination (the nearest existing ancestor with its symlinks resolved, plus the missing rest), so neither a symlink out of the project nor one to `.bb` passes. A directory that exists and has files is refused with `ErrExists`, so the learner's work is never overwritten. Any refusal writes nothing and leaves `experiment.yaml` untouched.
- **Spec.** The app is added through the YAML node tree with `path`, `command: [<name>]` and `uses`, and the whole spec is validated before any file is written.
- **Variables.** The names come from the resolved model, exactly as `bb up` injects them (`compose.AppEnv`), so the prefixing rule applies: with two Postgres services the later one's are `ANALYTICS_DATABASE_URL`, and so on. The result returns them per service; secret values never leave the core.

The template is `starters/go/app/` (embedded with the starters, every file `.tmpl`), rendered by `starters.RenderApp`. Go files are formatted with `go/format`. It writes one module with `main.go` at its root:

| File | Content |
|---|---|
| `main.go` | A header comment that maps the lab: each used service with its catalog title and summary, and each variable with what it holds, its value when not secret, or `(secret)`; plus `SSL_CERT_FILE`, `BB_FQDN`, `TLS_CERT_FILE`, `TLS_KEY_FILE` and `bb creds`. `main()` sits at the top: `requireEnv` checks every variable of the used services (exiting 1 with one line naming all missing ones), then calls small `new*` wiring functions below `main()` (`newPostgres`, `newRedis`, `newKafkaProducer` and `newKafkaConsumer` with demo topic `events` created via `kadm` and group `<app>-group`, `newS3`, `newConsul`, `newHTTPClient`) that connect using the exact injected env vars and verify connectivity (`Ping`, `ListBuckets`, `Status().Leader()`), logs ready, and waits on `SIGTERM` (`<-ctx.Done()`) so `bb up` starts healthy immediately. No fake logic interconnects services. |
| `go.mod`, `go.sum` | One canonical pair for every app, generated with `go mod tidy` against a file importing every client (including `franz-go/pkg/kadm`, `aws-sdk-go-v2`, and `aws-sdk-go-v2/credentials`), at the starters' versions; the module path is `example.com/<name>`. Unused requirements are harmless; `go mod tidy` trims them. |
| `Dockerfile` | The starters' shape: a `dev` stage on `golang:<GoImage>` that runs `go build -o /usr/local/bin/<name> .` with the build cache mount, and a non-root `prod` stage on `alpine:<AlpineVersion>`. |
| `README.md` | What the app is, the services and variables it can reach, how `main.go` wires and verifies each client before waiting on `SIGTERM`, how to run, rebuild, add a service later, and reach it from a browser (`ports`, `host`). |

Each catalog type maps to one client package in `starters.sdkImports` (a test fails for a type without one) and one wiring helper in `main.go` (one helper per catalog type, taking the exact injected env var names so prefixed multi-service labs reuse the same helper):

| Types | Client package & helper |
|---|---|
| `postgres`, `pgbouncer` | `github.com/jackc/pgx/v5/pgxpool` (`newPostgres`, verifies with `Ping`) |
| `redis` | `github.com/redis/go-redis/v9` (`newRedis`, verifies with `Ping`) |
| `kafka` | `github.com/twmb/franz-go/pkg/kgo` + `kadm` (`newKafkaProducer`, `newKafkaConsumer`; verifies with `Ping` and ensures demo topic `events` exists with 3 partitions) |
| `s3-compatible` | `github.com/aws/aws-sdk-go-v2/service/s3` (`newS3` with `UsePathStyle: true`, verifies with `ListBuckets`) |
| `consul` | `github.com/hashicorp/consul/api` (`newConsul`, verifies with `Status().Leader()`) |
| `otel` | `go.opentelemetry.io/otel` + `sdk/trace`, `sdk/metric`, `sdk/log`, `otlptracegrpc`, `otlpmetricgrpc`, `otlploggrpc`, `otelslog` (`newOtel`, initializes enabled signals and verifies with `ForceFlush`) |
| `opensearch` | `github.com/opensearch-project/opensearch-go/v5/opensearchapi` (`newOpenSearch`, verifies with `Info`) |
| `nats` | `github.com/nats-io/nats.go` (`newNats`, verifies with `Flush`) |
| `haproxy`, `envoy`, `traefik`, `nginx` | `net/http` (`newHTTPClient` + `<service>URL := os.Getenv(...)`) |

`TestRenderedAppBuilds` renders the scaffold for every catalog type at once, each type alone, no service, prefixed and plaintext uses, colliding and leading-digit service names, and app names `log` and `context`, and runs `go build` and `go vet` with `-mod=readonly`; it then runs the binary without its variables (one message, exit 1) and with dummy variables (verifying ready/SIGTERM for non-pinging apps and clean non-panic failure past `requireEnv` for pinging apps). `TestIntegrationAddApp` runs a real Postgres, Redis and Kafka lab with `AddApp` defaults and asserts `bb up` succeeds, the app logs ready, and Kafka's `events` topic exists.

## Starter list

| Id | Phase | Teaches |
|---|---|---|
| `kafka/producer-consumer` | 1 | Topics, partitions, keys, consumer groups, offsets, rebalancing. |
| `kafka/sink-to-postgres` | 2 | Consume → transform → write; idempotent inserts; at-least-once semantics; migrations. |
| `redis/cache-invalidation` | 2 | Cache-aside, invalidate-on-write via Kafka events, TTLs, stale reads. |
| `consul/service-discovery` (recommended) | 2 | Registration via a Consul sidecar, health-checked catalog, resolving services by name. |
| `consul/self-registration` (alternative) | 2 | The same lookup, with each service registering and deregistering itself through the Consul API: the plumbing the sidecar removes. |
| `s3/objects-basics` | 2 | Buckets, keys and prefixes, multipart upload, presigned URLs against S3 Compatible storage. |
| `sidecar/envoy-ambassador` | 5 | The ambassador pattern: client-side load balancing across replicas, retries and timeouts in an Envoy sidecar instead of app code. |
| `sidecar/envoy-tls-termination` | 5 | TLS termination at an Envoy sidecar, and why the app binds loopback only. |
| `sidecar/envoy-transparent` | 5 | Transparent proxying (iptables redirect, original destination), egress policy as the root of service ACLs, and how it differs from the ambassador. |
| `sidecar/circuit-breaking` | 5 | Envoy outlier detection: ejecting a failing replica, `max_ejection_percent`, ejection time and recovery. |
| `sidecar/pgbouncer-pool` | 5 | Connection pooling with a PgBouncer sidecar: transaction vs session mode, pool size vs client count. |
| `sidecar/log-shipper` | 5 | The adapter pattern: logs as files in a shared volume, shipped to S3 Compatible storage by a Fluent Bit sidecar. |
| `lb/round-robin` | 5 | Round robin with NGINX across replicas re-resolved through DNS; scaling replicas without restarting the proxy. |
| `lb/least-connections` | 5 | HAProxy `leastconn` vs `roundrobin` with uneven request cost: the slow replica gets fewer requests under least connections. |
| `lb/power-of-two-choices` | 5 | Traefik `p2c` vs `wrr`: a load-aware choice between two random servers with little state. |
| `lb/consistent-hashing` | 5 | Envoy `ring_hash` vs `maglev` keyed by a header: the same key on the same replica, cache locality, and how many keys move when a replica leaves. |
| `lb/sticky-sessions` | 5 | Cookie session persistence with HAProxy, and what happens to a user's session when their replica dies. |
| `lb/weighted-canary` | 5 | A canary release through a Traefik weighted service (v1 90 / v2 10); shifting and rolling back traffic. |
| `lb/blue-green` | 5 | Blue-green cutover through NGINX's graceful reload, with no failed request; reload vs restart. |
| `lb/health-checks` | 5 | Active (HAProxy) vs passive (Envoy outlier detection) health checks: time to eject and time to recover. |
| `lb/consul-traefik` | 5 | Service discovery with a load balancer: backends register in Consul through sidecar agents, and Traefik discovers and balances every healthy replica, with no proxy config to edit when replicas change. |
| `proxy/l4-vs-l7` | 5 | HAProxy `mode tcp` vs `mode http`: what each layer can see and route on; per-connection vs per-request balancing. |
| `proxy/tls-modes` | 5 | Re-encrypt, terminate and passthrough side by side at one HAProxy, and which certificate the client sees on each. |
| `proxy/request-mirroring` | 5 | Shadow traffic to v2 with Envoy `request_mirror_policies`; responses discarded, side effects not. |
| `proxy/connection-draining` | 5 | Removing a backend under load with no failed requests: HAProxy's runtime API `drain`, and a replica that drains itself on SIGTERM. |
| `pgbouncer/pooling-modes` | 5 | Session vs transaction pooling in a shared PgBouncer; session state and SQL `PREPARE` in transaction mode; central pool vs sidecar. |
| `gw/path-host-routing` | 5 | Routing at an API gateway by path prefix and by Host with Traefik; strip prefix, router priority and a default route. |
| `gw/request-transformation` | 5 | Traefik middlewares: headers added and removed both ways, prefix strip, path rewrite vs redirect, request IDs. |
| `gw/api-versioning` | 5 | API versions by path and by header through NGINX, a safe default version, and Deprecation, Sunset and Link headers on v1. |
| `gw/rate-limiting` | 5 | Token bucket (Envoy `local_ratelimit`) vs leaky bucket (NGINX `limit_req`) with the same rate and burst: a burst passed at once vs queued, 429s, per-client keys, local vs global limits. |
| `gw/circuit-breaking` | 5 | Envoy cluster circuit breakers: `max_connections`, `max_pending_requests` and `max_requests` overflow to fast 503s, capping a slow backend's concurrency. |
| `gw/retries-and-timeouts` | 5 | Envoy route and per-try timeouts, `retry_on` and back-off; retries hiding a flaky backend, and a retry storm against a down one, with and without a retry budget. |
| `gw/jwt-auth` | 5 | Authentication at the edge with Envoy `jwt_authn`: an issuer's JWKS fetched over TLS, 401 for missing or forged tokens, 403 for a token issued for another audience, verified claims forwarded as headers. |
| `gw/forward-auth` | 5 | Delegated authentication with Traefik `forwardAuth`: an auth service decides every request, returns identity headers, and revokes sessions at once. |
| `gw/bff-aggregation` | 5 | Backend for frontend behind Traefik: one call fanned out to two services concurrently and trimmed to the screen, vs gateway routing. |
| `gw/grpc-routing` | 5 | gRPC through Envoy over HTTP/2: routing by service and method, per-call balancing, a per-method deadline ending in `DEADLINE_EXCEEDED`. |
| `otel/traces` | 5+ | Multi-service W3C `traceparent` propagation over HTTPS with `otelhttp`, custom child spans, error recording, and Tempo (`signals: traces`, `host: true`). |
| `otel/metrics` | 5+ | Counters, UpDownCounters, explicit-bucket Histograms, `otelhttp`, and Go runtime metrics across replicas into Prometheus (`signals: metrics`, `host: true`). |
| `otel/logs` | 5+ | Multi-service `log/slog` structured logging bridged via `otelslog` to Loki (`signals: logs`, `host: true`). |
| `otel/multi-service` | 5+ | Correlated logs, metrics, and traces across four Go services, Postgres, and Kafka (with W3C trace propagation in Kafka headers) into full LGTM (`signals: logs,metrics,traces`, `host: true`). |
| `opensearch/search-basics` | 5+ | Explicit `text`/`keyword`/numeric mappings, bulk indexing of 40 Washington State DOL EV registrations (`data.wa.gov`), typo-tolerant BM25 queries, cached filters, and faceted aggregations (`host: true`). |
| `opensearch/postgres-cdc-sync` | 5+ | CQRS search read-model projection (`postgres` → `kafka` → `opensearch`) on 40 Washington State EV registrations with manual Kafka offset commits and live lag tracking (`host: true`). |
| `opensearch/zero-downtime-reindex` | 5+ | Immutable mapping migration on 40 Washington State EV registrations (`v1` → `v2`) via server-side `_reindex` and atomic `_aliases` swap while live queries run with zero errors (`host: true`). |
| `nats/core-pubsub-request-reply` | 5+ | Core NATS subject wildcards (`*`, `>`), zero-config load balancing via queue groups across replicas, and built-in request-reply RPC over TLS. |
| `nats/jetstream-workqueue` | 5+ | JetStream `WorkQueuePolicy` stream with `Nats-Msg-Id` deduplication, a shared Pull Consumer across replicas, `NakWithDelay` retries, and delete-on-ack. |
| `nats/jetstream-kv-watch` | 5+ | JetStream Key-Value bucket with revision history, optimistic concurrency (`kv.Update` CAS conflict rejection), and zero-polling `WatchAll` across replicas. |
