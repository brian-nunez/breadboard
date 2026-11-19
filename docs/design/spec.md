# Experiment spec (`experiment.yaml`)

`experiment.yaml` is the **source of truth** for an experiment. The CLI, UI, and MCP server all read and edit this file; edits go through a YAML node tree so comments and ordering survive.

The spec holds **intent only**. It never contains secrets, certificates, or generated values, so it is safe to commit. Everything omitted takes a catalog default.

## Example

```yaml
version: 1
name: kafka-lab

services:                    # infra services, from the catalog
  kafka:
    type: kafka
  postgres:
    type: postgres
    resources:               # optional; overrides catalog defaults
      memory: 512m
      cpus: 0.5

apps:                        # user code; one compose service each
  producer:
    path: ./orders           # directory containing the Dockerfile
    command: [producer]      # the binary the dev stage built (D-056)
    uses: [kafka]
  consumer:
    path: ./orders
    command: [consumer]
    uses: [kafka, postgres]
    ports:                   # optional; published on 127.0.0.1 only
      - 8080
```

## Fields

| Field | Required | Notes |
|---|---|---|
| `version` | yes | Schema version. Currently `1`. Older versions load; `bb migrate` rewrites the file to the current one; a newer version is refused. |
| `name` | yes | Lowercase DNS label (`[a-z0-9-]`, ≤ 40 chars). Becomes the compose project `bb-<name>` and part of each service identity. |
| `services.<name>.type` | yes | A catalog entry id. The map key is the service name and its DNS label. |
| `services.<name>.resources` | no | `memory` (Docker size string), `cpus` (decimal). |
| `services.<name>.config` | no | Type-specific options defined by the catalog entry (e.g. Postgres database name). Validated by the entry. |
| `services.<name>.upstreams` | no | Proxy types only (`haproxy`, `nginx`, `pgbouncer`, …): the services and apps the proxy sits in front of, each with its own TLS mode. See [Proxies and upstreams](#proxies-and-upstreams). |
| `services.<name>.host` | no | `true`: reach the service from the host at `https://<name>.<experiment>.localhost:<port>` through the lab's host router. Only infra with an HTTPS listener (proxies, Consul, S3 Compatible). `false` is the same as leaving it out. See [Host access](#host-access). |
| `services.<name>.file` | no | Proxy types only: a native config file, relative to `experiment.yaml`, that replaces the generated one. See [Proxies and upstreams](#proxies-and-upstreams). |
| `apps.<name>.path` | yes | Relative to the spec file. Must contain a `Dockerfile` with a `dev` stage (checked by `bb up` and `bb export`). Compile the app in that stage and run the binary as the `command`, as the starters do; then the app is the container's main process and receives SIGTERM. |
| `apps.<name>.command` | no | Overrides the image command. |
| `apps.<name>.uses` | no | Infra services this app connects to. Each entry creates an edge, injects that service's env contract, and adds `depends_on: service_healthy`. When two used services share an env variable name (two Postgres services, say), the later one's variables are prefixed with its name: `ANALYTICS_DATABASE_URL`. |
| `apps.<name>.ports` | no | Container ports to publish on `127.0.0.1`. |
| `apps.<name>.host` | no | `true` or `{port: <n>}`: reach the app from the host at `https://<app>.<experiment>.localhost:<port>`, and each replica at `https://<app>-<n>.<experiment>.localhost:<port>`. The app must serve TLS: on `port`, else its Envoy `inbound.port`, else its `register.port`. Works with replicas. `false` is the same as leaving it out. See [Host access](#host-access). |
| `apps.<name>.health` | no | Opt into an HTTP health check (`path`, default `/healthz`; `port`, default 8080; the port is injected as `BB_HEALTH_PORT`). Without it, the check is that the container's main process is running, so app code needs no health endpoint. |
| `apps.<name>.register` | no | Register the app in a service registry (Consul) through a sidecar agent, so app code does not register itself. `port` (required): the port the app serves TLS on. `name`: the registered service name, default the app's name. `check`: a path for an HTTPS health check; without it the check is a TCP connection that completes a TLS handshake. `tags`: a list of tags registered with the service, the same on every replica, so a registry consumer (Traefik's Consul catalog provider) can select and configure it. Requires a registry service in `uses`; the sidecar is a service named `<app>-agent`, which takes that name in the namespace. |
| `apps.<name>.replicas` | no | Number of instances, 1 to 10 (default 1). With more than one, the app runs as `<app>-1` … `<app>-N`, each reachable as `<app>-<n>.<domain>` and all of them as `<app>.<domain>` (a DNS alias on every instance). Such an app cannot publish `ports`. See [Replicas](#replicas). |
| `apps.<name>.sidecars` | no | Containers that share the app's network namespace and a directory with it; the app becomes a pod. See [Sidecars](#sidecars). |
| `apps.<name>.resources` | no | As for services. Default `256m` and `0.5` cpus: room for a running Go app. Compiling happens while the image builds, outside this limit. An app that instead runs `go run` at start compiles under the limit and needs much more (`github.com/redis/go-redis/v9` alone takes over 1 GiB). |

Service and app names share one namespace and must be unique DNS labels. The name `bb-host` is reserved for the host router. Names the engine generates for an app take their place in it too: `<app>-<n>` for replicas, `<app>-pod` and `<app>-<sidecar>` for a pod, `<app>-netinit` in transparent mode, `<app>-agent` for `register` (with replicas, each instance's names: `<app>-<n>-pod`, `<app>-<n>-envoy`, `<app>-<n>-agent`, …).

## `uses` entries and TLS overrides

A `uses` entry is a service name (short form) or an object (long form). The long form is how an edge deviates from the TLS default; the rules live in [`security.md`](security.md).

```yaml
uses:
  - kafka
  - service: postgres
    tls:
      mode: off
      reason: practice TLS termination at a pooler
```

Reserved for later phases, rejected by v1 validation: `tls.clientAuth` (mTLS), `tls.ca` (multiple CAs).

## Proxies and upstreams

A proxy is an infra service whose catalog entry is a proxy type (see [`catalog.md`](catalog.md#proxies-phase-5-wave-b)). Its `upstreams` are the backends it sits in front of: apps (every replica) or other infra services. Apps reach the proxy with `uses: [edge]`, like any infra service.

```yaml
services:
  edge:
    type: haproxy
    upstreams:
      - web                     # short form: re-encrypt to web's TLS port
      - service: api            # an app (all its replicas) or an infra service
        port: 9090              # optional: see below
        name: v1                # optional: route name, default the service name
        tls:
          mode: off             # terminate: a plaintext hop on a private segment
          reason: practice TLS termination
      - service: payments
        tls: {mode: passthrough}  # L4, routed by SNI, never decrypted
    file: ./edge/haproxy.cfg    # optional: your own native config replaces the generated one
apps:
  client:
    path: ./client
    uses: [edge]                # EDGE_URL=https://edge.bb.internal:8443
```

| Field | Required | Notes |
|---|---|---|
| `upstreams[]` | no | A target name (short form) or a mapping. An HTTP proxy without upstreams starts and answers `503`; `pgbouncer` requires exactly one. |
| `upstreams[].service` | yes | An app or an infra service, other than the proxy itself, listed once per proxy. |
| `upstreams[].tls.mode` | no | `on` (default): **re-encrypt**; the proxy decrypts, then connects to the target over TLS and verifies it against the lab CA with SNI set to the target's name. `off`: **terminate**; the hop is plaintext on a private segment and needs a `reason`. `passthrough`: the proxy routes the client's TLS session by SNI on its passthrough port without decrypting, so the client sees the target's own certificate. The catalog entry lists the modes it supports. |
| `upstreams[].port` | no | App targets only (an infra target is reached on its catalog listener). For `on` and `passthrough`, the port the app serves TLS on; default its Envoy `inbound.port` or `register.port`, and an app with neither must set it. For `off`, the plaintext port the app listens on, default `8080`. |
| `upstreams[].name` | no | The route name, a DNS label unique within the proxy; default the service name. With one decrypted upstream every request goes to it; with several, requests under `/<name>/` go to that upstream with the prefix stripped, and the first one also takes every other path. |
| `file` | no | Copied into the run directory at each `bb up` and mounted in place of the generated config; the certificate (`/etc/bb/tls/`), the lab CA (`/etc/bb/ca.pem`) and the trust bundle stay mounted. The catalog entry says what the file must keep (HAProxy and NGINX: the loopback endpoint their health checks read; Envoy: the admin API on `127.0.0.1:15000`; PgBouncer: the auth and HBA files). For Traefik the file replaces the dynamic (routing) configuration `dynamic.yml` only; the static configuration and the certificate store stay generated. |
| `static_file` | no | Proxy types whose catalog entry has a static configuration to extend; only `traefik` in v1. A path relative to `experiment.yaml` to a YAML mapping of Traefik static-configuration sections, merged into the generated `traefik.yml` at each `bb up`: an edit changes the config hash and recreates the proxy, and a missing file is an error naming the path. `entryPoints`, `providers`, `api`, `ping` and `global` are Breadboard's and are refused, matched without regard to case as Traefik matches them; a top-level key must be a plain string: a merge key (`<<`), a key with a non-string tag (`!!binary …`), an alias or an anchored key is refused too. Aliases in values are expanded before sections are placed. `log` and `accessLog` replace Breadboard's (`bb logs` shows only what goes to stdout). Every other section (`tracing`, `metrics`, `experimental`, `serversTransport`, ...) is added as given. Works with or without `file`. |

**Terminate hops to apps.** For an `off` upstream to an app, every replica joins the private segment `plain-<app>` with a static address, the alias `<app>.plain.<domain>` and its own `<instance>.plain.<domain>`, and receives `BB_PLAIN_LISTEN_ADDR=<its segment address>:<port>`. The app should bind its plaintext listener to that address; whether it does or not, the app runs as a pod whose init container drops the plaintext port on every other address, so the proxy reaches it and no other container can ([`security.md`](security.md#proxy-and-relay-modes-phase-5)). The pod adds the generated names `<instance>-pod` and `<instance>-netinit` to the namespace. Every terminating proxy of one app uses the same port. The edge is reported as a plaintext warning like any `tls.mode: off` edge.

**Passthrough targets** must speak TLS from the first byte (HTTPS, Kafka, Redis, Consul): the proxy reads the server name from the TLS ClientHello. Clients connect to the proxy's passthrough address (`<NAME>_PASSTHROUGH_ADDR`) and must send the target's name as SNI (`<target>.<domain>`). Postgres negotiates TLS after a plaintext request, so a Postgres client needs direct TLS (`sslnegotiation=direct`, libpq 17 and later) to pass through.

Variables an app receives from a proxy it uses (the service name upper-cased, `-` → `_`):

| Variable | Value |
|---|---|
| `<NAME>_URL` | `https://<name>.<domain>:8443` |
| `<NAME>_PASSTHROUGH_ADDR` | `<name>.<domain>:9443`, when an upstream is `passthrough` |

**A shared Postgres pool.** `pgbouncer` is a proxy type with exactly one upstream, a `postgres` service, in mode `on` (default: PgBouncer connects with `verify-full`) or `off` (with a `reason`: plaintext over the Postgres segment, like a plaintext `uses` edge). It takes no `port` and no passthrough. `config.pool_mode` (`session`, `transaction` or `statement`; default `transaction`) and `config.pool_size` (server connections per database and user, 1 to 1000; default `10`) tune it. An app that uses it receives the Postgres contract pointing at the pool, with the Postgres service's application role (see [`catalog.md`](catalog.md#pgbouncer)):

```yaml
services:
  postgres:
    type: postgres
  pool:
    type: pgbouncer
    config: {pool_size: 5}
    upstreams: [postgres]
apps:
  orders:
    path: ./orders
    uses: [pool]                # DATABASE_URL=postgres://app:…@pool.bb.internal:6432/app?sslmode=verify-full
```

## Replicas

```yaml
apps:
  api:
    path: ./api
    replicas: 3
```

renders three services, `api-1`, `api-2` and `api-3`. Each has its own certificate with DNS SANs for its own name and the shared name, its own health check and status row, and receives `BB_SERVICE` and `BB_FQDN` with its own name. The app's `uses` edges and env contract are the same for every instance. `bb logs api` streams every instance; `bb logs api-2` one. With `replicas: 1` (or none) the service stays `api`. Decision: [ADR-0016](../adr/README.md).

## Host access

```yaml
services:
  edge:
    type: haproxy
    upstreams:
      - service: api
        port: 8443        # an upstream app names its TLS port; host.port does not set it
    host: true
apps:
  api:
    path: ./api
    replicas: 3
    host:
      port: 8443          # the port the app serves TLS on
```

Browsers and curl resolve every `*.localhost` name to loopback, so a lab with `host` targets is reachable from the host by name: `https://edge.<experiment>.localhost:<port>`, `https://api.<experiment>.localhost:<port>` (every replica) and `https://api-2.<experiment>.localhost:<port>` (one replica). The lab gets one router, the service `bb-host`, published on `127.0.0.1:<port>`; the port is chosen once per lab and printed by `bb up` and `bb status`. The router's certificate comes from the install's host CA, which `bb ca` shows and explains how to trust; behind it every hop is TLS verified against the lab CA ([`security.md`](security.md#host-access-phase-5-wave-e)). Only HTTP over TLS is routed; TCP services keep using `ports` on an app or a client inside the lab. Decision: [ADR-0017](../adr/README.md).

## Sidecars

A sidecar is a container that shares its app's network namespace (same address, same `localhost`) and the directory `/var/run/bb` (`BB_SHARED_DIR` in the app's environment). An app with at least one sidecar runs as a **pod**: a small holder container, `<app>-pod`, owns the network, and the app and its sidecars join it; the sidecars are healthy before the app starts ([`architecture.md`](architecture.md#pods)). Traffic between an app and its own sidecars stays on loopback and may be plaintext; every hop between containers stays TLS ([`security.md`](security.md)). Types come from the sidecar catalog ([`catalog.md`](catalog.md#sidecar-catalog)).

Every entry has a `type` and an optional `name` (default: the type), unique within the app; the sidecar's service is `<app>-<name>`. Keys depend on the type, and unknown keys are errors.

```yaml
apps:
  api:
    path: ./api
    uses: [postgres, s3]
    sidecars:
      - type: envoy
        inbound:              # TLS termination in front of the app
          port: 8443          # TLS port other containers connect to (the app's certificate)
          app_port: 8080      # plaintext port the app listens on, loopback only
        outbound:             # ambassador listeners on 127.0.0.1
          - service: orders   # an app, or an infra service with an HTTPS listener
            port: 9001        # local plaintext port; the app gets ORDERS_URL=http://127.0.0.1:9001
            lb: round_robin   # round_robin | least_request | random | ring_hash | maglev
            timeout: 5s
            retries: 2
            outlier: {consecutive_5xx: 5, ejection: 30s, max_ejection_percent: 10, interval: 10s}
        transparent: false    # true | {egress: allow | deny}
        file: ./envoy.yaml    # optional: your own Envoy config replaces the generated one
      - type: pgbouncer
        service: postgres     # a postgres service in uses
        pool_mode: transaction  # session | transaction | statement
        port: 6432            # loopback port; DATABASE_URL and PG* point here
        pool_size: 10
      - type: fluent-bit
        to: s3                # an s3-compatible service in uses
        bucket: logs          # created if missing
```

| Field | Required | Notes |
|---|---|---|
| `type` | yes | `envoy`, `pgbouncer`, or `fluent-bit`. (`consul-agent` is internal: `register` creates it.) |
| `name` | no | Lowercase DNS label; default the type. |
| `envoy.inbound` | no | `port` and `app_port`, both required, different. The app receives `BB_LISTEN_ADDR=127.0.0.1:<app_port>`. To publish the app on the host, put `inbound.port` (never `app_port`) in `ports`. |
| `envoy.outbound[]` | no | `service` and `port` required. A target app must serve TLS: its own Envoy `inbound.port`, or its `register.port`. Envoy verifies the target against the lab CA and balances across all its replicas. Defaults: `lb: round_robin`, `timeout: 5s`, `retries: 2`, no outlier detection. `outlier` (Envoy outlier detection) requires `consecutive_5xx` (at least 1) and `ejection` (the base ejection time, a positive duration); `max_ejection_percent` (1-100, the most of the cluster ejected at once) defaults to Envoy's 10, which ejects no host of a cluster under 10 hosts, and `interval` (the analysis sweep that returns ejected hosts, a positive duration) to Envoy's 10s. One listener per target per app. |
| `envoy.transparent` | no | Captures all of the app's TCP traffic. `true` means `{egress: allow}`; `deny` refuses every destination but the declared outbound targets and the services in `uses`, matched by port and address (the lab then gets fixed container addresses). From outside the pod only declared listeners answer: Envoy's `inbound.port` and the app's `register.port`; a published port must be one of them. Envoy runs as the reserved uid 1337 and a registration agent as 1338; `bb up` refuses an app whose image starts as either (or as a user name it cannot resolve), and the app runs without `SETUID`/`SETGID` and with `no-new-privileges`. The app can then call a target by name in plain HTTP, `http://<target>.<domain>:<tls port>`, and the wire is TLS; the `<TARGET>_URL` loopback listener keeps working. Only on `envoy`. |
| `envoy.file` | no | Path relative to `experiment.yaml`. Its content is copied into the run directory at each `bb up` (mounted at `/etc/bb/config/envoy.yaml`) and replaces the generated config; certificates stay at `/etc/bb/tls/` and the CA at `/etc/bb/ca.pem`. The file must keep the admin listener on `127.0.0.1:15000` (the health check reads `/ready`) and, with `transparent`, listeners on 15001 and 15006. |
| `pgbouncer.service` | yes | A postgres service in `uses`, reached over TLS (no `tls.mode: off`). One pooler per service per app. |
| `pgbouncer.pool_mode`, `port`, `pool_size` | no | Defaults `transaction`, `6432`, `10`; pool size 1 to 1000. |
| `fluent-bit.to` | yes | An s3-compatible service in `uses`. |
| `fluent-bit.bucket` | yes | An S3 bucket name, added to the service's buckets so it exists. The app writes `*.log` files to `/var/run/bb/logs/` (`BB_LOG_DIR`); each upload is one object `<instance>/<yyyy>/<mm>/<dd>/<hhmmss>-<uuid>.jsonl`, at most 10 seconds after the line was written. |

Variables a sidecar adds to or changes in its app's environment:

| Type | Variables |
|---|---|
| `envoy` | `BB_LISTEN_ADDR=127.0.0.1:<app_port>` with `inbound`; `<TARGET>_URL=http://127.0.0.1:<port>` per outbound target (name upper-cased, `-` → `_`) |
| `pgbouncer` | the pooled service's `DATABASE_URL` → `postgres://<user>:<password>@127.0.0.1:<port>/<db>?sslmode=disable`, `PGHOST=127.0.0.1`, `PGPORT=<port>`, `PGSSLMODE=disable` (prefixed names for a second Postgres service) |
| `fluent-bit` | `BB_LOG_DIR=/var/run/bb/logs` |

Every pod's app also receives `BB_SHARED_DIR=/var/run/bb`.

`register` is unchanged. With `sidecars` too, its agent runs in the pod like any other sidecar.

## Validation rules

Validation fails with every violation listed, each with its YAML path:

- Unknown top-level or nested keys are errors (catches typos).
- `type` must exist in the catalog; `config` must satisfy the entry's schema.
- Every `uses` target must exist and be an infra service; no target may be listed twice.
- `tls.mode` is `on` or `off`; `off` is allowed only toward entries with a plaintext listener.
- `tls.mode: off` requires a non-empty `reason`.
- Reserved fields are rejected with a message naming the phase that introduces them.
- `register` requires a registry service in `uses`. Each of `register.tags` is non-empty, has no leading or trailing whitespace, and is listed once.
- Proxies: `upstreams` and `file` are accepted only on proxy types, and `static_file` only on proxy types that support it (`traefik`); `file` and `static_file` are relative paths that do not point into `.bb` and hold no colon, comma or newline. Each upstream names an existing app or service other than the proxy, at most once; route names are unique DNS labels; the mode is one the type supports; `off` requires a `reason`, and toward an infra service a plaintext listener; an HTTP proxy decrypts (`on`, `off`) only HTTP targets (apps, or infra with an HTTPS listener) and takes other TLS services as `passthrough`; `port` is not allowed on infra targets; an app target of `on` or `passthrough` needs a TLS port (its own or `port`); an `off` app target is not in transparent mode, its plaintext port differs from its TLS and health ports, and every terminating proxy of that app uses the same port. A type may restrict targets to catalog types, require a minimum number of upstreams (`pgbouncer`: one) and cap the number.
- `replicas` is between 1 and 10; an app with more than one replica cannot publish `ports`.
- Sidecars: `type` must be a sidecar catalog type; keys must belong to that type (`transparent` only on `envoy`); names are unique within the app; no two containers of one pod bind the same port (the app's `inbound.app_port`, Envoy's `inbound.port`, outbound ports, admin `15000`, transparent `15001`/`15006`, PgBouncer's `port`, Fluent Bit's `2020`, the registration agent's `8301`/`8501`); `pgbouncer.service` is a postgres service and `fluent-bit.to` an s3-compatible service in the app's `uses`; `outbound.service` names an existing app or HTTPS infra service other than the app itself. The app's own ports (`health.port`, default 8080 with `health`; `register.port` unless it is an Envoy `inbound.port`; `inbound.app_port`) are claimed before any sidecar's, and the error names where each was declared. A variable a sidecar gives the app (`BB_LISTEN_ADDR`, `<TARGET>_URL`, `BB_LOG_DIR`) must not share a name with one the app already receives from Breadboard, a used service or another sidecar (for example an outbound target named `database` beside Postgres's `DATABASE_URL`); PgBouncer's rewrite of its own service's variables is allowed.
- `host`: on an app, `port` (when given) is a valid port and, in transparent mode, a declared listener (Envoy's `inbound.port` or `register.port`); without `port` the app needs an Envoy `inbound.port` or a `register.port`. On a service, the type has an HTTPS listener and `port` is not allowed. No service or app is named `bb-host`, and no name the engine generates for an app is `bb-host` (app `bb` with a sidecar named `host`, say).
- Every name the engine generates for an app is unique in the shared namespace and at most 63 characters.
