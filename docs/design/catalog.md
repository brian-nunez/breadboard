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
| Storage | Named volume for data; removed by `bb down --volumes`, including volumes of services removed from `experiment.yaml` since an earlier `bb up` (matched by the `breadboard.experiment-id` label). |
| Env contract | Variables injected into every app that `uses` it, named the way the ecosystem expects. |
| Learning notes | Embedded markdown: what you got, key concepts, things to try, things to break. Shown by `bb learn` and in the UI. |
| Explorer probes | (Phase 3) How the UI reads live state. |
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

- Official `postgres` image, `ssl=on`, and `pg_hba.conf` accepts only `hostssl` (plus `host` on a private segment when an edge asks for plaintext).
- Gotcha: the key file must be `0600` and owned by the image's postgres uid. The entry's start script copies the mounted key with `install -m 0600 -o postgres` before handing over to the image's entrypoint; the learning notes explain it.
- The app role and database are created by an init script on first start; changing `config.user` or `config.database` later needs `bb down --volumes`. Data lives in a named volume at `/var/lib/postgresql` (Postgres 18 layout).
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
- **Registration sidecar.** An app with `register` in `experiment.yaml` gets a service `<app>-agent`: a Consul client agent in the app's network namespace (`network_mode: service:<app>`), so it has the app's address and presents the app's certificate. It joins the server over encrypted gossip and mutually authenticated RPC, registers the app with a check (a TCP connection with a verified TLS handshake by default, or HTTPS on `register.check`), and leaves the cluster when it stops, which deregisters the app. Its HTTPS API listens on `127.0.0.1` only; it is healthy once the registration reaches the server's catalog. This is the recommended way to register; apps can also call the Consul API themselves (`consul/self-registration`).
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

### Proxies and relays (Phase 5)

Traefik, Envoy, HAProxy, PgBouncer.
