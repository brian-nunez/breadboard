# Breadboard for agents

Breadboard (`bb`) is a local lab for practising system design with real services. An **experiment** is a directory with an `experiment.yaml`; bb turns it into a Docker Compose project where every service is the real thing (Kafka, Postgres, Redis, Consul, an S3 compatible store), with TLS on by default, plus the user's own app code.

## The pieces

- **`experiment.yaml`** is the source of truth and holds intent only: no secrets, certificates or generated values, so it is safe to commit. Tools edit it through a YAML node tree, so the user's comments and ordering survive. Prefer the tools to editing it by hand.
- **Infra services** come from the catalog (`catalog`). Each runs an exact image pin with a health check and memory and CPU limits.
- **Apps** are the user's code: a directory with a `Dockerfile` whose `dev` stage compiles the program; the binary named in `command` runs as the container's main process.
- **Starters** (`starters`, `starter_details`) are working example apps that use each technology's official client library. `apply_starter` writes the code and adds the apps and services they need.
- **`.bb/`** in the project holds generated state: secrets (`secrets.json`), certificates, and the rendered compose files. It is never committed. Deleting it resets credentials.

## Networking and TLS

- Each service is reachable from the experiment's containers as `<service>.<domain>` (default domain `bb.internal`), for example `postgres.bb.internal:5432`. Nothing is published to the host unless the spec asks for it, and then only on `127.0.0.1`.
- Breadboard runs its own CA (`ca`). Every service and app gets a certificate for its name, mounted at `/etc/bb/tls/tls.crt` and `/etc/bb/tls/tls.key`. The CA is at `/etc/bb/ca.pem`, and `SSL_CERT_FILE` points at a trust bundle (`/etc/bb/trust-bundle.pem`) holding it. A second CA, `host` in the `ca` result, signs only the host router's certificate for `https://<service>.<experiment>.localhost:<port>`; containers never see it.
- Connections use TLS unless an app's `uses` entry turns it off with `tls: {mode: off, reason: ...}` (only toward services with a plaintext listener).

## How apps connect

An app lists the services it needs in `uses`. For each one it receives that service's environment contract, for example `DATABASE_URL` and `PG*` for Postgres, `KAFKA_BROKERS` and `KAFKA_SECURITY_PROTOCOL` for Kafka, `REDIS_URL` for Redis, `CONSUL_HTTP_ADDR` for Consul, and `AWS_*` with `S3_ENDPOINT` for S3. Every app also gets `TLS_CERT_FILE`, `TLS_KEY_FILE`, `BB_SERVICE` and `BB_FQDN`. `catalog` lists the variable names per type; `creds` shows their values; `service_details` shows a service's whole setup.

Registering an app in Consul: add `register: {port: <tls port>}` to the app and Consul to its `uses`; bb adds a sidecar agent (`<app>-agent`) that registers it, so the app code does not register itself.

Several instances of an app: `replicas: N` (1 to 10) runs `<app>-1` … `<app>-N`, each reachable as `<app>-<n>.<domain>` and all of them as `<app>.<domain>`; `status` lists each instance (`app`, `instance`, `replicas`), `logs` with the app's name streams all of them, and `service_details` for the app describes its first instance and lists every instance in `replicas`. An app with replicas cannot publish `ports`.

Sidecars: `sidecars` on an app adds containers that share its network namespace and `/var/run/bb` (edit `experiment.yaml`). `envoy` terminates TLS in front of the app (`inbound: {port, app_port}`; the app binds `BB_LISTEN_ADDR`), proxies its loopback calls to other apps or HTTPS services over TLS (`outbound: [{service, port}]`; the app gets `<TARGET>_URL`), and with `transparent` captures all its traffic. `pgbouncer` pools a Postgres service in `uses` (the app's `DATABASE_URL` points at `127.0.0.1:6432`). `fluent-bit` ships files the app writes to `BB_LOG_DIR` to a bucket on an S3 compatible service in `uses`.

Seeing sidecars: in `status` each app instance lists its `sidecars`, and each sidecar's row follows it with its own state and health and a `sidecar` object: its type, the `ports` it binds in the pod (`loopback` ones only the app reaches), the `env` it adds to the app, the app variables it `rewrites` (PgBouncer's `DATABASE_URL`), its settings, and for Envoy whether it is `transparent` with which `egress`. An Envoy outbound target shows as an edge in the app's `uses` with `via` (the sidecar) and `to_app`. The pod's namespace holder (`<instance>-pod`) is never listed; when it is down, the app reports its state. `envoy_clusters` reads a sidecar's live view from Envoy's admin API: every upstream host with the replica at that address, health flags, outlier ejection, and request, error and connection counts, so you can see which replica got traffic and why one fails. `pgbouncer_pools` shows a PgBouncer sidecar's client and server connections, queries and wait times. Both take an app, an instance (`api-2`), or the sidecar's service name (`api-2-envoy`).

Proxies: the infra types `haproxy`, `envoy`, `traefik` and `nginx` (HTTP proxies) and `pgbouncer` (a Postgres pool shared by apps) sit in front of other services. A proxy lists its backends in `upstreams` (an app with all its replicas, or an infra service), each with a TLS mode: `on` (re-encrypt, the default: the proxy decrypts, then opens verified TLS to the backend), `off` (terminate: plaintext from the proxy to the backend on a private segment only they share, with a required `reason`), or `passthrough` (the client's TLS session forwarded by SNI on port 9443, so the client sees the backend's own certificate). An app reaches a proxy with `uses` and gets `<NAME>_URL=https://<name>.<domain>:8443` (and `<NAME>_PASSTHROUGH_ADDR` with a passthrough upstream); a pool gives the Postgres contract. With one HTTP upstream every request goes to it; with several, `/<route>/` reaches each with the prefix stripped and the first also takes every other path. `file` replaces the generated native config; on `traefik`, `static_file` is merged into its static configuration (metrics, tracing, plugins; never `entryPoints`, `providers`, `api`, `ping` or `global`).

Seeing proxies: in `status` a proxy's row lists its upstream edges in `uses`, each with `mode` (`on`, `off`, `passthrough`), the target port and `to_app`; a terminate edge has `tls: false` and its `reason` and appears in the warnings. `service_details` for a proxy lists `upstreams` with mode, port, instances and the route (path prefix, default route, or SNI). The explorers read each proxy's own view inside its container: `haproxy_stats` (each backend's servers: the replica at each address, UP/DOWN, last check, sessions and picks), `traefik_routes` (routers and services with each server's UP/DOWN; no per-server counts), `nginx_status` (stub_status totals and what each upstream resolves to; open-source NGINX has no per-server counters), `envoy_clusters` for an infra `envoy` (each upstream cluster's hosts with requests and errors), and `pgbouncer_pools` for an infra `pgbouncer` (clients from every app sharing the pool, and server connections to Postgres).

Host access: `host: true` on an infra service with an HTTPS listener (a proxy, Consul, an S3 compatible store), or `host: {port: <tls port>}` on an app (optional when it has an Envoy `inbound` or `register` port), makes it reachable from the host at `https://<target>.<experiment>.localhost:<port>`; an app with replicas also answers per instance at `https://<app>-<n>.<experiment>.localhost:<port>`. bb adds one router, the infra service `bb-host` (Traefik; `traefik_routes` reads it), published on `127.0.0.1:<port>`, a port chosen once per lab. It terminates TLS with a certificate from the host CA (`host` in the `ca` result) and re-encrypts to every replica, verified against the lab CA, keeping the `Host` header and adding `X-Forwarded-*`. `up` and `status` return `host_urls` (each with its `target`, and `instance` for a one-replica URL) and `host_port`; in `status` and `service_details` a target's row has its own `host_urls` and the router's row has `host_router`. Only HTTP over TLS is routed; Postgres, Redis and Kafka are reached through an app. Browsers and curl resolve `*.localhost` to loopback; other clients may need `curl --resolve` or an `/etc/hosts` line. The user trusts the host CA themselves (`bb ca` shows how); never change their trust store.

## Usual workflow

1. `init` creates the experiment (or restores `.bb/` in a fresh clone).
2. `add_service` adds infra, or `apply_starter` adds a working example with its infra.
3. `up` builds apps, starts everything and waits until every service is healthy and stays healthy. It fails if a service is unhealthy or restarts while starting.
4. Inspect: `status` (state, health, CPU, memory, sidecars, replicas), `logs`, `creds`, `service_details`, and the explorers: `postgres_tables`, `redis_keys`, `consul_catalog`, `kafka_topics`, `s3_buckets`, for proxies `haproxy_stats`, `traefik_routes` and `nginx_status`, and for Envoy and PgBouncer (sidecars or infra services) `envoy_clusters` and `pgbouncer_pools`.
5. `down` stops it; `down` with `volumes` also deletes the data.

Editing `experiment.yaml` or app code takes effect on the next `up`.

## Debugging

- `up` failed: read its error (it lists the progress so far), then `logs` for the failing service, and `status` for restarts or `oom_killed`.
- An app cannot connect: compare its environment (`service_details` for the app, `creds` for values) with what the code reads; check the edge in `status` (`tls` and port).
- Data questions: the explorers read live data with each service's own CLI inside its container.
- Traffic through sidecars: `envoy_clusters` on the calling app shows each target replica's request and error counts and whether outlier detection ejected it; on the target app it shows whether requests reached the app on loopback. `logs` for `<instance>-envoy` prints Envoy's access log.
- Traffic through a proxy: the proxy's explorer (`haproxy_stats`, `envoy_clusters`, `traefik_routes`, `nginx_status`) shows which backends are up and, where the proxy counts it, how many requests each replica got; `logs` for the proxy prints its access log. A backend that never comes up: check the upstream's port and mode in `service_details` (passthrough needs a backend that serves TLS itself; terminate needs the app to listen on `BB_PLAIN_LISTEN_ADDR`).
- `experiment.yaml is invalid`: every violation is listed with its YAML path.
- Another bb command running on the same experiment makes a tool fail with "busy"; retry once it finishes.

## Limits of this server

- Tools act on the experiment containing the directory `bb mcp` was started in. `dir` may name another directory inside it, never one outside.
- `logs` returns recent lines; it does not follow.
- Credentials are local, generated per experiment, and only valid inside it.
- `bb mcp --read-only` exposes only the tools that change nothing the user owns.
