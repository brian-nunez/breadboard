# Catalog

A catalog entry is everything Breadboard knows about one infrastructure service type. Adding a service type means adding one entry and nothing else in the engine.

## Entry contract

Every entry defines:

| Part | Requirement |
|---|---|
| Image | Official image, **exact version pin** (never `latest` or a major-only tag). The pin lives in the entry's Go code; that code is the source of truth for versions. |
| Listeners | Protocol, port, TLS configuration, and how to enable a plaintext listener when an edge asks for one ([`security.md`](security.md)). An entry whose server cannot confine a plaintext port to a private segment offers none, and validation rejects `tls.mode: off` edges to it. |
| Config | Server configuration with TLS on, plus any type-specific `config` options the spec may set. |
| Secrets | Which secrets it needs and how they reach the server. |
| Health check | Exercises the real protocol **over TLS with verification**, so "healthy" also proves the certificates work. |
| Resources | Default `mem_limit` and `cpus`. |
| Storage | Named volume for data; removed by `bb down` (kept by `bb down --keep-volumes`), including volumes of services removed from `experiment.yaml` since an earlier `bb up` (matched by the `breadboard.experiment-id` label). |
| Env contract | Variables injected into every app that `uses` it, named the way the ecosystem expects. |
| Learning notes | Embedded markdown: what you got, key concepts, things to try, things to break. Shown by `bb learn` and in the UI. |
| Explorer probes | (Phase 3) How the UI reads live state. |
| Proxy | (Phase 5, proxies only) The upstream TLS modes it supports, whether it routes HTTP, which target types it takes, and the name of its native config file. See [Proxies](#proxies-phase-5-wave-b). |
| Exporter mappings | (Phase 7+) How each exporter represents it. |

Every service also gets `restart: unless-stopped`, the `breadboard.*` labels, and its `<service>.<domain>` alias.

## Phase 1 entries

Hostnames below use the default domain and a service named after its type. Image pins as of Phase 1 (the Go code in `internal/catalog` remains the source of truth):

| Entry | Image | Limits | Plaintext listener |
|---|---|---|---|
| `kafka` | `apache/kafka:4.3.2` | 1g, 1.0 cpu | 9092, bound to the segment address |
| `postgres` | `postgres:18.7-alpine3.24` | 512m, 0.5 cpu | same port 5432; `hostnossl` limited to the segment subnet |
| `redis` | `redis:8.10.3-alpine3.23` | 256m, 0.25 cpu | none in v1 |
| `consul` | `hashicorp/consul:1.22.7` | 256m, 0.5 cpu | 8500, bound to the segment address |
| `s3-compatible` (Phase 2) | `chrislusf/seaweedfs:4.53` | 512m, 0.5 cpu | none |

Every container mounts its certificate at `/etc/bb/tls/tls.crt`, its key at `/etc/bb/tls/tls.key`, both together at `/etc/bb/tls/keystore.pem`, the CA at `/etc/bb/ca.pem`, the trust bundle at `/etc/bb/trust-bundle.pem`, and entry configuration under `/etc/bb/config/`.

### `kafka`

- Official `apache/kafka` image, single node in KRaft combined mode (broker + controller), no ZooKeeper. The entry bypasses the image's environment-driven configuration: it mounts its own `server.properties`, formats the log directory with a generated cluster id (`kafka-storage.sh format --ignore-formatted`), then starts the server. The controller listener is loopback-only.
- SSL listener on 9093 with PEM certificates directly (`ssl.keystore.type=PEM`), so no Java keystores.
- Internal topic replication factors set to 1 for the single node.
- `auto.create.topics.enable=false`: starters create topics explicitly with the admin client, which teaches partitions and replication.
- Health check: `kafka-broker-api-versions.sh` over SSL against the FQDN, with a 128 MB heap so it fits beside the broker's 512 MB heap.
- Env contract:
  - `KAFKA_BROKERS=kafka.bb.internal:9093`
  - `KAFKA_SECURITY_PROTOCOL=SSL`
- No client authentication in v1.
- Risk: slowest to start and most sensitive to listener config. Build it first in Phase 1.

### `postgres`

- Official `postgres` image, `ssl=on`, and `pg_hba.conf` accepts only `hostssl` (plus `hostnossl` limited to the private segment's subnet when an edge asks for plaintext).
- Gotcha: the key file must be `0600` and owned by the image's postgres uid. The entry's start script copies the mounted key with `install -m 0600 -o postgres` before handing over to the image's entrypoint; the learning notes explain it.
- The app role and database are created by an init script on first start; changing `config.user` or `config.database` later needs `bb down`. Data lives in a named volume at `/var/lib/postgresql` (Postgres 18 layout).
- Secrets: superuser password; default database and app role from `config` (defaults `app`/`app`).
- Health check: a TLS connection with `sslmode=verify-full` running `select 1`.
- Env contract:
  - `DATABASE_URL=postgres://app:<pw>@postgres.bb.internal:5432/app?sslmode=verify-full`
  - `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE=verify-full`, `PGSSLROOTCERT=/etc/bb/ca.pem` (so `psql` and libpq verify too; pgx also verifies through `SSL_CERT_FILE`)
  - On a plaintext edge: host `postgres.plain.bb.internal` and `sslmode=disable`.

### `redis`

- Official `redis` image, `tls-port 6379`, `port 0`, password on the default user, append-only persistence.
- No plaintext listener in v1: Redis binds every port to the same addresses, so a plaintext port could not be confined to a private segment. Plaintext Redis arrives with terminating proxies in Phase 5.
- Health check: `redis-cli --tls --cacert … -h redis.bb.internal ping`.
- Env contract: `REDIS_URL=rediss://default:<pw>@redis.bb.internal:6379/0`

### `consul`

- Official `hashicorp/consul` image, single server in dev-like topology with persistence, HTTPS on 8501, plain HTTP disabled.
- Other listeners: server RPC (8300) requires TLS with a client certificate from the CA; LAN gossip (8301) stays on for Phase 2 client agents and is encrypted with a generated gossip key (`-encrypt` on the command line, never in the config file); DNS, gRPC, and WAN gossip are off.
- `enable_agent_tls_for_checks` is on, so TLS health checks of services registered directly with the server verify against the lab CA.
- Health check: `consul operator raft list-peers` over HTTPS with CA verification; it succeeds only once a leader exists.
- Env contract:
  - `CONSUL_HTTP_ADDR=https://consul.bb.internal:8501`
  - `CONSUL_CACERT=/etc/bb/trust-bundle.pem`
  - On a plaintext edge: `CONSUL_HTTP_ADDR=http://consul.plain.bb.internal:8500` only.
- **Registration sidecar.** An app with `register` in `experiment.yaml` gets a service `<app>-agent`: a Consul client agent in the app's network namespace (`network_mode: service:<app>`), so it has the app's address and presents the app's certificate. It joins the server over encrypted gossip and mutually authenticated RPC, registers the app with a check (a TCP connection with a verified TLS handshake by default, or HTTPS on `register.check`) and the tags in `register.tags`, if any (each replica's agent registers its own instance, `<app>-<n>.<domain>`, with the same tags), and leaves the cluster when it stops, which deregisters the app. Its HTTPS API listens on `127.0.0.1` only; it is healthy once the registration reaches the server's catalog. This is the recommended way to register; apps can also call the Consul API themselves (`consul/self-registration`). Underneath, the agent is the internal `consul-agent` sidecar type ([Sidecar catalog](#sidecar-catalog)); in an app that also has `sidecars`, it runs in the app's pod.
- ACLs are out of scope for v1.

## Later entries

### `s3-compatible` (Phase 2)

- Displayed as **S3 Compatible**: an object store speaking the S3 API, deliberately not branded as AWS S3. Implemented with the official SeaweedFS image `chrislusf/seaweedfs:4.53` (`weed server -s3`), single container. It replaces MinIO, whose community edition is archived and no longer publishes images.
- **One network listener.** The S3 gateway serves HTTPS only on 8333 with the Breadboard certificate (a plain HTTP request is refused). Master, volume and filer bind to `127.0.0.1`; the Iceberg and Lance endpoints are off.
- **gRPC is mutual TLS.** The S3 gateway's gRPC port (18333) shares its bind address and cannot be disabled, so a generated `/etc/seaweedfs/security.toml` requires a client certificate from the lab CA on every gRPC port and, through `allowed_commonNames`, accepts only the service's own certificate. Other lab certificates are rejected.
- **Credentials.** A generated access key pair is passed as `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, which SeaweedFS uses as its admin identity when no IAM config exists.
- **Buckets.** `config.buckets` (comma-separated) lists buckets created at startup, once the gateway answers.
- **No plaintext listener.** The gRPC port would follow any plaintext bind address, so `tls.mode: off` is refused, as for Redis.
- Health check: an authenticated `ListBuckets` (`curl --aws-sigv4`) over TLS against the FQDN.
- Env contract (generic names first, plus the variables official AWS SDKs read, so S3 clients work with zero code config):
  - `S3_ENDPOINT=https://s3-compatible.bb.internal:8333`, `S3_REGION=us-east-1`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_FORCE_PATH_STYLE=true`
  - `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`

### Proxies (Phase 5, wave B)

A proxy is an infra entry with `Proxy` set. It sits in front of the apps and services listed in its `upstreams` ([`spec.md`](spec.md#proxies-and-upstreams)), with a TLS mode per upstream: re-encrypt, terminate or passthrough ([`security.md`](security.md#proxy-and-relay-modes-phase-5)). The engine does everything around the proxy; the entry only turns resolved upstreams into its native config.

| Part | What the engine provides | What the entry does |
|---|---|---|
| Validation | Reads the entry's `Proxy` (modes, `HTTP`, target types, upstream cap) through `spec.TypeInfo.Proxy`; no proxy is named in validation code. | Declares `Proxy`. |
| Upstreams | `Server.Upstreams`, resolved: route `Name`, `Service`, `App`, `Mode`, `Reason`, `Host` (a name resolving to every instance on the hop's network: `<app>.<domain>`, or `<app>.plain.<domain>` for terminate), `Servers` (each instance's own name on that network), `ServerName` (the name to send as SNI and verify), `Port`. Helpers `L7` and `Passthroughs` split them. | Renders a frontend per listener and a backend per upstream. |
| Listeners | The 8443/9443 convention (`ProxyHTTPSPort`, `ProxyPassthroughPort`); `HTTPProxyListeners()` for edges. | Listens on 8443 with its certificate, and on 9443 only when an upstream is passthrough. |
| Env contract | `ProxyClientEnv`: `<NAME>_URL=https://<name>.<domain>:8443`, plus `<NAME>_PASSTHROUGH_ADDR=<name>.<domain>:9443` with a passthrough upstream. | Sets `Client: ProxyClientEnv` (HTTP proxies). |
| Segments | Private segments and static addresses for terminate hops; `BB_PLAIN_LISTEN_ADDR` for app targets. | Connects to `Host` for terminate hops. |
| `file` | Reads the user's file at every render and swaps it in for the file named `Proxy.ConfigFile` (`Entry.Render`); copied into the bundle, so an edit changes the config hash. | Names its config file. |
| Storage | None: a proxy with no `DataPath` gets no data volume. | Leaves `DataPath` empty. |
| User text | `catalog.Comment(prefix, text)`: every line of a `tls.reason` (or any other user-derived text) gets the comment marker; CR/LF normalised, control characters blanked. | Renders every user-derived string through it, never with a bare format verb. |
| Live listeners | `Entry.ListenersFor(upstreams)` adds 9443 when an upstream is passthrough; service details show it. | Nothing. |

Proxies do not wait for their upstreams to be healthy: an app behind a proxy may also use it, and the proxy discovers backends through DNS and active health checks at runtime. Every container still has a health check, `mem_limit` and `cpus`; admin and stats endpoints bind to `127.0.0.1` only.

| Entry | Image | Limits | Modes | Health check |
|---|---|---|---|---|
| `haproxy` | `haproxy:3.4.7` (Debian: bash, openssl, no curl) | 128m, 0.25 cpu | re-encrypt, terminate, passthrough | loopback `monitor-uri` answers 200, and a TLS handshake on 8443 verifies the certificate (`openssl s_client -verify_return_error`) |
| `envoy` | `envoyproxy/envoy:v1.40.2` (Ubuntu: bash, openssl, no curl) | 128m, 0.25 cpu | re-encrypt, terminate, passthrough | admin `GET /ready` on `127.0.0.1:15000` over bash `/dev/tcp`, and a TLS handshake on 8443 verifies the certificate (`openssl s_client -verify_return_error`) |
| `traefik` | `traefik:v3.7.19` (Alpine: wget, nc, no curl or openssl) | 128m, 0.25 cpu | re-encrypt, terminate, passthrough | `traefik healthcheck --ping` against `ping` on the loopback `traefik` entrypoint |
| `nginx` | `nginx:1.30.6-alpine` (stream and `ssl_preread` built in; wget and curl, no openssl) | 64m, 0.25 cpu | re-encrypt, terminate, passthrough | loopback `/healthz` answers 200 (`wget`), and `curl --cacert` completes a verified handshake on 8443 with a `TRACE` that NGINX refuses itself (405), so no backend is called |
| `pgbouncer` | `edoburu/pgbouncer:v1.26.1-p0` (Alpine, runs as `postgres`, has `psql` and `pg_isready`) | 64m, 0.25 cpu | `on` (verify-full), `off` (Postgres segment); one `postgres` upstream | `pg_isready` on its own name with `sslmode=verify-full` |

#### `haproxy`

- Config `/etc/bb/config/haproxy.cfg`, generated and commented (a header lists every route); the image's entrypoint runs `haproxy -W -db -f` on it. Logs go to stdout (`bb logs <name>`).
- `global`: `maxconn 256` and `nbthread 1`. Without `maxconn`, HAProxy sizes its connection tables from the file-descriptor limit (about a million inside Docker): about 90 MiB at idle in a 128m container. With the limits, after TLS traffic in every mode the container used about 25 MiB (`memory.current`; the worker's RSS about 31 MiB). A load test raises both in its own copy (`file`) and gives the service more memory (`resources`).
- `frontend https`: `bind :8443 ssl crt /etc/bb/tls/keystore.pem alpn h2,http/1.1`, sets `X-Forwarded-Proto: https` and `X-Forwarded-For`. One decrypted upstream takes every request; with several, `use_backend` on `path_beg /<name>/` and `http-request replace-path` strips the prefix, and the first upstream is `default_backend`. With none, requests get 503.
- One backend per upstream, `balance roundrobin`, a `server-template <name> <replicas> <host>:<port>` re-resolved through `resolvers docker` (`nameserver 127.0.0.11:53`), `init-addr none` so a backend that is not up yet starts down instead of failing the proxy, and active checks (`check inter 2s fall 3 rise 2`; a TCP connect, with a verified TLS handshake for re-encrypt).
- Re-encrypt: `ssl verify required ca-file /etc/bb/ca.pem sni str(<target>.<domain>) verifyhost <target>.<domain> check-sni <target>.<domain>`. The explicit `sni` matters: HAProxy 3.4's default `sni-auto` would otherwise send the client's `Host` (the proxy's own name).
- Terminate: plain `check` to `<target>.plain.<domain>:<port>` on the segment.
- Passthrough: `frontend passthrough` on `:9443` in `mode tcp`, `tcp-request inspect-delay 5s`, accepts once a ClientHello arrives (`req.ssl_hello_type 1`) and routes `req.ssl_sni -i <target>.<domain>` to a TCP backend; an unknown name is closed.
- `frontend monitor` on `127.0.0.1:8404`: `monitor-uri /healthz` for the health check and the stats page at `/stats` with `stats show-legends` (so the CSV carries each server's address). Unreachable from other containers.
- Explorer (`HAProxyStats`, MCP `haproxy_stats`, the web UI's "HAProxy stats" tab): `GET /stats;csv` over bash `/dev/tcp` through `docker exec`, columns read by name; per backend (named after its upstream, `be_<route>` or `be_tls_<route>`) each `server-template` slot with the replica at its address (`getent hosts` on every instance name inside the container; slot numbers are not replica numbers), status, last check, sessions, picks, bytes and errors.
- `file`: must keep the monitor frontend on `127.0.0.1:8404` (the health check reads it) and a TLS listener on 8443.
- Env contract: `<NAME>_URL`, and `<NAME>_PASSTHROUGH_ADDR` with a passthrough upstream.

#### `envoy`

The infra `envoy` is a standalone proxy; the `envoy` sidecar (below) is a separate type in the sidecar catalog.

- Config `/etc/bb/config/envoy.yaml`, a static bootstrap, generated and commented (a header lists every route); the command is `envoy -c … --concurrency 1`, run as the image's envoy user. Access logs go to stdout.
- Listener `https` on `:8443`: TLS with the proxy's certificate (ALPN `h2,http/1.1`, TLS 1.2 minimum) and an HTTP connection manager with `use_remote_address: true`, so Envoy sets `X-Forwarded-Proto` and appends `X-Forwarded-For`. One decrypted upstream takes `prefix: /`; with several, `prefix: /<name>/` with `prefix_rewrite: /` per upstream, then `prefix: /` to the first. With none, a `direct_response` 503.
- One cluster per upstream: strict DNS (`cluster_type` `envoy.clusters.dns`, D-162) on the target's shared name (every replica's address, re-resolved every 5s), `ROUND_ROBIN`, and an HTTP health check (`GET /` every 2s, `Host` the target's name, healthy for any status below 500, out after 3 failures, back after 2, `no_traffic_interval: 2s`).
- Re-encrypt: an UpstreamTlsContext with `sni: <target>.<domain>`, `trusted_ca: /etc/bb/ca.pem` and an exact DNS SAN match on the same name. Health checks use it too.
- Terminate: a plaintext cluster on `<target>.plain.<domain>:<port>`.
- Passthrough: listener `passthrough` on `:9443` with the `tls_inspector`; one filter chain per upstream matched on `server_names`, each a TCP proxy to cluster `tls_<route>` (TCP connect health check). A name no chain matches is closed.
- Admin on `127.0.0.1:15000` (the sidecar's port, so the Envoy explorer reads both): `/ready` for the health check, `/clusters` and `/stats` for explorers. `EnvoyClusters` (MCP `envoy_clusters`, the web UI's "Envoy clusters" tab) takes the infra service: each cluster is role `upstream` with the upstream's target and TLS mode, and hosts on a terminate segment map to their instances.
- `file`: must keep the admin API on `127.0.0.1:15000` and a TLS listener on 8443.
- Env contract: `<NAME>_URL`, and `<NAME>_PASSTHROUGH_ADDR` with a passthrough upstream.

#### `traefik`

- The file provider, plus the Consul catalog provider when `config.consul_catalog` is set (below); never the Docker socket. Static config `/etc/bb/config/traefik.yml` (Breadboard's): entrypoints `websecure` (`:8443`, TLS on for every router), `passthrough` (`:9443`, only with a passthrough upstream) and `traefik` (`127.0.0.1:8080`: the API and dashboard with `api.insecure`, and `ping`); `aliasHeadersStrategy: delete` and `encodedCharacters` with all seven options `false` (400 for an encoded `/`, `\`, NUL, `;`, `%`, `?` or `#` in the path; D-204) on each; logs and access logs to stdout; the file provider on `/etc/bb/traefik` with `watch`.
- Dynamic config in `/etc/bb/traefik`: `tls.yml` (Breadboard's: the default certificate of the `default` TLS store, `minVersion: VersionTLS12`) and `dynamic.yml` (the routes, generated and commented).
- Routers on `websecure`: one decrypted upstream on ``PathPrefix(`/`)``; with several, ``PathPrefix(`/<name>/`)`` with a `stripPrefix` middleware (`/<name>`) per upstream and a fallback on ``PathPrefix(`/`)`` to the first; Traefik's rule-length priority puts the prefixes first. Router names are in their own namespaces, so no route name collides with another router: `route-<name>` for each upstream's route (the single upstream's included) and `default-<first>` for the fallback. With none, router `no-upstream` to a service without servers answers 503, unless `consul_catalog` is set: then no router is generated (a catch-all would shadow the discovered routes), and sections left empty are omitted, because Traefik refuses an empty `routers` or `http` ("cannot be a standalone element").
- The file provider runs every dynamic file through Go templates, comments included, before reading the YAML. Generated `dynamic.yml` writes each `{{` as `{{"{{"}}`, which the template pass turns back into `{{`, so a reason with template text stays literal. A user's `file` is not rewritten: its templates run.
- One service per upstream listing every instance (`https://<app>-<n>.<domain>:<port>`), since Traefik does not discover several addresses behind one name; `strategy: wrr` (Traefik 3.7 also offers `p2c`, `hrw` and `leasttime`). App upstreams get `healthCheck` (`GET /` every 2s, `hostname` the target's name, 2xx or 3xx healthy); infra upstreams (one server) get none.
- Re-encrypt: a `serversTransport` per upstream with `serverName: <target>.<domain>` and `rootCAs: [/etc/bb/ca.pem]`.
- Terminate: `http://<instance>.plain.<domain>:<port>` for each instance, on the segment.
- Passthrough: TCP routers ``HostSNI(`<target>.<domain>`)`` with `tls.passthrough: true` on `passthrough`, to TCP services listing every instance with a TCP connect health check; an unclaimed name is closed.
- Explorer (`TraefikRoutes`, MCP `traefik_routes`, the web UI's "Traefik routes" tab): `/api/{http,tcp}/{routers,services}` on the loopback `traefik` entrypoint with the image's `wget` through `docker exec`; routers (rule, entry points, middlewares, TLS terminate or passthrough) and services with each server's `serverStatus` (UP/DOWN), Traefik's `@internal` ones left out. The API has no per-server request counter; the access log has each request.
- `config.consul_catalog: <consul service>` (D-225): Traefik also routes what that Consul service's catalog declares. The value must name a `consul` service of the experiment (`services.<name>.config.consul_catalog`). `traefik.yml` gains `providers.consulCatalog` (`exposedByDefault: false`, `refreshInterval: 5s`, `endpoint` `address: <consul>.<domain>:8501`, `scheme: https`, `tls.ca: /etc/bb/ca.pem`) and a global `serversTransport` with `rootCAs: [/etc/bb/ca.pem]`, which verifies the discovered servers (`https://<app>-<n>.<domain>:<port>`, the address each replica's agent registers) by the name in their URL. The service waits for Consul to be healthy (`depends_on`), so its first read of the catalog succeeds. An app opts in through `register.tags`: `traefik.enable=true`, a router rule (``traefik.http.routers.<name>.rule=PathPrefix(`/`)``; with no entry point tag the router joins Traefik's default entry points, which leave out the loopback `traefik` one, so it answers on `websecure`) and `traefik.http.services.<name>.loadbalancer.server.scheme=https`, without which Traefik speaks plain HTTP to the TLS port. Breadboard draws the links: `<traefik> → <consul>` "discovers", and `<traefik> → <app>` "routes to" for every app registered with that Consul service with the tag `traefik.enable=true`; they carry no new network or TLS setting.
- `file` replaces `dynamic.yml` only; `traefik.yml` and `tls.yml` stay generated, so the entrypoints, the certificate and the health check keep working.
- `static_file` extends `traefik.yml` (`catalog.Proxy.StaticFile` and `MergeStatic`, read by spec as `ProxyInfo.StaticFile`; D-206, D-207): a YAML mapping whose sections are merged into the generated document, which keeps its own comments. `entryPoints`, `providers`, `api`, `ping` and `global` are refused (matched without regard to case, like Traefik; a top-level `<<` too, and any top-level key that is not a plain string: a key with a non-string tag, an alias or an anchored key; aliases in values are expanded before sections move); `log` and `accessLog` replace Breadboard's; `serversTransport` is refused while `consul_catalog` is set (it verifies the discovered servers); every other section is appended as given under a comment naming the file. Prometheus metrics belong on the loopback entry point, `metrics.prometheus.entryPoint: traefik`, read with `docker exec <edge> wget -qO- http://127.0.0.1:8080/metrics`. Sections that send data out (tracing or metrics exporters, `experimental.plugins` downloaded from the plugin catalog) need a collector in the lab or network egress. The host router `bb-host` has no spec service and takes none.
- Env contract: `<NAME>_URL`, and `<NAME>_PASSTHROUGH_ADDR` with a passthrough upstream.

#### `nginx`

- Config `/etc/bb/config/nginx.conf`, generated and commented (a header lists every route); the container runs `nginx -c` on it directly (`daemon off`), skipping the image's `conf.d` scripts. One worker, so round robin is easy to watch. Logs go to stdout and stderr (`bb logs <name>`); the health check's requests are left out of the access log by user agent.
- `http`: `resolver 127.0.0.11 valid=5s ipv6=off`. One `upstream` per decrypted upstream with a `zone`, `server <host>:<port> resolve max_fails=3 fail_timeout=10s` (open source since 1.27.3, which re-resolves the shared name, so every replica is balanced round robin and NGINX follows scaling) and `keepalive 16`. Passive health only: `health_check` is NGINX Plus.
- `server` on `listen 8443 ssl` with `http2 on`, the certificate and key, TLS 1.2 and 1.3; `proxy_http_version 1.1`, an empty `Connection` header, the client's `Host`, `X-Forwarded-For` and `X-Forwarded-Proto: https`. One decrypted upstream is `location /`; with several, each gets `location /<name>/` with `proxy_pass <scheme>://be_<name>/` (the trailing `/` replaces the prefix) and the first also takes `location /`. With none, `return 503`.
- Re-encrypt: `proxy_pass https://…`, `proxy_ssl_verify on`, `proxy_ssl_trusted_certificate /etc/bb/ca.pem`, `proxy_ssl_verify_depth 2`, `proxy_ssl_server_name on`, `proxy_ssl_name <target>.<domain>`, `proxy_ssl_session_reuse on`.
- Terminate: `proxy_pass http://…` to an upstream resolving `<target>.plain.<domain>` on the segment.
- Passthrough: a `stream` block on `listen 9443` with `ssl_preread on` and `map $ssl_preread_server_name` to one stream `upstream` per target (`zone`, `resolve`, `max_fails`); unknown names map to `bb_unknown_sni`, whose only server is `down`, so the connection is closed.
- Loopback `server` on `127.0.0.1:8404`: `location = /healthz` (200) for the health check and `location = /stub_status` for explorers. Unreachable from other containers.
- Explorer (`NginxStatus`, MCP `nginx_status`, the web UI's "NGINX status" tab): `stub_status` with the image's `wget` (active, reading, writing, waiting; accepts, handled, requests) and, per upstream, what each instance's name resolves to inside the container (`getent hosts` one name at a time: musl's stops at the first miss). Open-source NGINX has no per-upstream or per-server counters (the NGINX Plus API), and the UI says so.
- `file`: must keep the status server on `127.0.0.1:8404` and a TLS listener on 8443. Open source balancing methods a copy can switch to: `least_conn`, `ip_hash` (http only), `hash <key> consistent`, `random two least_conn`, `sticky cookie` (since 1.29.6).
- Env contract: `<NAME>_URL`, and `<NAME>_PASSTHROUGH_ADDR` with a passthrough upstream.

#### `pgbouncer`

- A connection pool shared by every app that uses it, in front of exactly one `postgres` upstream (`Proxy.Targets`, `MinUpstreams: 1`, `MaxUpstreams: 1`; modes `on` and `off`). Validation reports `services.<name>.upstreams` when it is missing, empty or longer, so `bb add pgbouncer` alone is refused: add the service with its upstream in `experiment.yaml`. Config `/etc/bb/config/pgbouncer.ini`, generated and commented, plus `pgbouncer_hba.conf`. Config keys: `pool_mode` (default `transaction`) and `pool_size` (`default_pool_size`, default 10, 1 to 1000). `bb add pgbouncer` lists the experiment's Postgres as the upstream when there is exactly one.
- `[databases]`: `<db> = host=<postgres>.<domain> port=5432 dbname=<db>`, the Postgres service's `config.database`.
- Clients: `listen_addr = *`, `listen_port = 6432`, no Unix socket, `client_tls_sslmode = require` with the service's certificate and key, TLS 1.2 and 1.3.
- Server side: `on` is `server_tls_sslmode = verify-full` with `server_tls_ca_file = /etc/bb/ca.pem` (the host name in `[databases]` is checked); `off` connects to `<postgres>.plain.<domain>` on the Postgres segment with `server_tls_sslmode = disable`, and Postgres's `hostnossl` line for that segment admits it.
- Credentials: Postgres provisions one application role per service (`config.user`, its generated `password`), which every app of that Postgres receives; apps of the pool log in as that role too. The container gets the role and a reference to the password in its environment (`BB_PG_USER`, `BB_PG_PASSWORD`) and writes `/tmp/pgbouncer/userlist.txt` at start with `umask 077`, like the PgBouncer sidecar; no generated file holds the password. `auth_type = hba`, so the HBA file decides: the admin console (`pgbouncer` database) from `127.0.0.1` over TLS only, every other line `hostssl … scram-sha-256`. `admin_users` and `stats_users` are the role.
- Env contract (the Postgres names, so drivers work unchanged): `DATABASE_URL=postgres://<user>:<password>@<name>.<domain>:6432/<db>?sslmode=verify-full`, `PGHOST=<name>.<domain>`, `PGPORT=6432`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE=verify-full`, `PGSSLROOTCERT=/etc/bb/ca.pem`. An app that uses both the pool and Postgres gets the second set prefixed, as for two Postgres services.
- Explorer: `PgBouncerPools` (MCP `pgbouncer_pools`, the web UI's "PgBouncer pools" tab) takes the infra service and runs the sidecar's script unchanged (`psql -h 127.0.0.1 -p 6432` as `$BB_PG_USER`, which negotiates TLS); pool mode and size come from `config`, the pooled service from the upstream.

Envoy and PgBouncer also run as sidecars (below).

## Sidecar catalog

A sidecar type is a container that runs inside an app's pod: it shares the app's network namespace and the directory `/var/run/bb`, presents the app's certificate, and is healthy before the app starts ([`spec.md`](spec.md#sidecars), ADR-0015). The sidecar catalog is separate from the infra catalog and lives in `internal/catalog/sidecar`; each type defines its exact image, resources, and a `Render` hook that returns its container, the changes it makes to the app's environment, the infra services it waits for, and an optional init container. Every container has a health check, `mem_limit` and `cpus`.

| Type | Image | Limits | Health check |
|---|---|---|---|
| `envoy` | `envoyproxy/envoy:v1.40.2` (Ubuntu-based: bash, no curl) | 128m, 0.25 cpu | admin `GET /ready` on `127.0.0.1:15000` over bash `/dev/tcp` |
| `pgbouncer` | `edoburu/pgbouncer:v1.26.1-p0` (Alpine: `pg_isready`, `psql`) | 64m, 0.25 cpu | `pg_isready -h 127.0.0.1 -p <port>` |
| `fluent-bit` | `fluent/fluent-bit:5.2.0-debug` (bash; the distroless tag cannot run a check) | 64m, 0.25 cpu | HTTP `GET /api/v1/health` on `127.0.0.1:2020` over bash `/dev/tcp` |
| `consul-agent` (internal) | the registry entry's image, `hashicorp/consul:1.22.7` | 128m, 0.25 cpu | the registration has reached the server's catalog |

The pod holder runs `alpine:3.24.4` (16m, 0.05 cpu, health check `true`); transparent mode's init container is built from a generated Dockerfile (`FROM alpine:3.24.4`, `RUN apk add --no-cache iptables`, written to `build/<app>-netinit/` in the run directory; 32m, 0.1 cpu). Generated sidecar configs are written to `config/<app>-<sidecar>/` and mounted under `/etc/bb/config/`.

### `envoy`

Runs as uid 101 (the image's entrypoint drops to its `envoy` user), or as the reserved uid 1337 in a transparent pod (`ENVOY_UID`), which transparent rules exempt. Config: `/etc/bb/config/envoy.yaml`, generated with a header naming the sidecar; access logs on stdout (`bb logs <app>-envoy`).

- **Inbound:** an HTTP listener on `0.0.0.0:<inbound.port>` with the app's certificate (ALPN `h2`, `http/1.1`) that forwards to `127.0.0.1:<app_port>` and sets `X-Forwarded-Proto: https`. The app receives `BB_LISTEN_ADDR=127.0.0.1:<app_port>` and binds loopback only, so nothing bypasses the sidecar.
- **Outbound:** a listener `outbound_<target>` on `127.0.0.1:<port>` that proxies to the target over TLS, verifying it against the lab CA with SNI and an exact SAN match on the target's shared name. The cluster finds every address of that name through the DNS cluster extension (`cluster_type: envoy.clusters.dns`, `V4_ONLY`, re-resolved every 5 s; D-203), so it balances across every replica. Knobs: `lb` (`round_robin` default; `ring_hash` and `maglev` hash on the source address), `timeout` (5s), `retries` (2, on connect failure, reset, refused stream and 5xx), `outlier` (off unless set: `consecutive_5xx`, `ejection` as `base_ejection_time`, `max_ejection_percent` (10) and `interval` (10s), both written out with Envoy's defaults when omitted). The app receives `<TARGET>_URL=http://127.0.0.1:<port>` (the target's name upper-cased, `-` replaced by `_`). A target app serves TLS on its own Envoy `inbound.port` or its `register.port`. The sidecar waits for infra targets to be healthy.
- **Transparent:** see [`security.md`](security.md#transparent-mode-phase-5). Listener `transparent_outbound` on 15001 uses the `original_dst` listener filter: plain HTTP to a declared target's TLS port is routed by `Host` and gets TLS origination; anything else passes through (`egress: allow`) or, with `egress: deny`, is allowed only to a declared target's addresses (plain HTTP or the app's own TLS) and to each `uses` service's address and port, and is otherwise closed (`403` for other hosts on a target's address). Listener `transparent_inbound` on 15006 forwards connections to the app's own TLS port (`register.port`) to `127.0.0.1:<port>` and closes any other.
- **`file`:** a user-owned native Envoy config replaces the generated one. It is copied into the run directory at each `bb up`; it must keep the admin listener on `127.0.0.1:15000` and, with `transparent`, listeners on 15001 and 15006.
- Admin API on `127.0.0.1:15000`: the health check reads `/ready`, and the Envoy explorer (`EnvoyClusters`, MCP `envoy_clusters`, the web UI's Envoy tab) reads `/clusters?format=json`, `/stats` and `/listeners?format=json` through `docker exec` (D-093). Status reports the container's health, not the admin API. In a transparent pod the admin port is unreachable from outside the pod.

### `pgbouncer`

Connects to its Postgres service with TLS `verify-full` against the lab CA, using the app's generated Postgres credentials. The app's `DATABASE_URL` and `PG*` variables for that service are rewritten to `127.0.0.1:<port>` with `sslmode=disable` (loopback inside the pod): `PGHOST=127.0.0.1`, `PGPORT=<port>`, `PGSSLMODE=disable`; the user, password and database stay the app's. Config `/etc/bb/config/pgbouncer.ini`: listens on `127.0.0.1` only, `auth_type = scram-sha-256`, `pool_mode` and `default_pool_size` from the spec, `max_client_conn = 200`, the app's role as admin and stats user (`psql -h 127.0.0.1 -p <port> pgbouncer -c 'SHOW POOLS'`). The auth file is written to `/tmp/pgbouncer/userlist.txt` inside the container at start, from the password in its environment, so the generated `pgbouncer.ini` holds no password. The environment itself follows the bundle's secret policy ([`security.md`](security.md)): the run directory's compose file carries the value (`bb up`), an export carries a `${VAR}` reference resolved from its `.env` file.

### `fluent-bit`

Adapter pattern: the app writes `*.log` files to `/var/run/bb/logs/` (`BB_LOG_DIR`; the directory is created writable by any user); Fluent Bit tails them and ships them to the S3 compatible service over TLS (certificate and hostname verified against the lab CA), into `bucket`, which is added to the service's buckets so it is created at startup. Each upload is one object `<instance>/<yyyy>/<mm>/<dd>/<hhmmss>-<uuid>.jsonl`, one JSON line per log line (`date`, `file`, `log`), uploaded at most 10 seconds after the line was written. Read offsets live in the shared directory, so a restart does not ship lines twice.

### `consul-agent` (internal)

The registration agent behind `register` (see [`consul`](#consul)), rendered by the registry's catalog entry. Not declarable in `sidecars`.
