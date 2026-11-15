# Catalog

A catalog entry is everything Breadboard knows about one infrastructure service type. Adding a service type means adding one entry and nothing else in the engine.

## Entry contract

Every entry defines:

| Part | Requirement |
|---|---|
| Image | Official image, **exact version pin** (never `latest` or a major-only tag). The pin lives in the entry's Go code; that code is the source of truth for versions. |
| Listeners | Protocol, port, TLS configuration, and how to enable a plaintext listener when an edge asks for one ([`security.md`](security.md)). |
| Config | Server configuration with TLS on, plus any type-specific `config` options the spec may set. |
| Secrets | Which secrets it needs and how they reach the server. |
| Health check | Exercises the real protocol **over TLS with verification**, so "healthy" also proves the certificates work. |
| Resources | Default `mem_limit` and `cpus`. |
| Storage | Named volume for data; removed by `bb down --volumes`. |
| Env contract | Variables injected into every app that `uses` it, named the way the ecosystem expects. |
| Learning notes | Embedded markdown: what you got, key concepts, things to try, things to break. Shown by `bb learn` and in the UI. |
| Explorer probes | (Phase 3) How the UI reads live state. |
| Exporter mappings | (Phase 7+) How each exporter represents it. |

Every service also gets `restart: unless-stopped`, the `breadboard.*` labels, and its `<service>.<domain>` alias.

## Phase 1 entries

Hostnames below use the default domain and a service named after its type.

### `kafka`

- Official `apache/kafka` image, single node in KRaft combined mode (broker + controller), no ZooKeeper.
- SSL listener on 9093 with PEM certificates directly (`ssl.keystore.type=PEM`), so no Java keystores.
- Internal topic replication factors set to 1 for the single node.
- `auto.create.topics.enable=false`: starters create topics explicitly with the admin client, which teaches partitions and replication.
- Health check: broker API versions request over SSL against the FQDN.
- Env contract:
  - `KAFKA_BROKERS=kafka.bb.internal:9093`
  - `KAFKA_SECURITY_PROTOCOL=SSL`
- No client authentication in v1.
- Risk: slowest to start and most sensitive to listener config. Build it first in Phase 1.

### `postgres`

- Official `postgres` image, `ssl=on`, and `pg_hba.conf` accepts only `hostssl` (plus `host` on a private segment when an edge asks for plaintext).
- Gotcha: the key file must be `0600` and owned by the image's postgres uid. The entry handles this; the learning notes explain it.
- Secrets: superuser password; default database and app role from `config` (defaults `app`/`app`).
- Health check: a TLS connection with `sslmode=verify-full` running `select 1`.
- Env contract:
  - `DATABASE_URL=postgres://app:<pw>@postgres.bb.internal:5432/app?sslmode=verify-full`
  - `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE=verify-full`

### `redis`

- Official `redis` image, `tls-port 6379`, `port 0` (plaintext disabled unless an edge asks), password via ACL default user.
- Health check: `redis-cli --tls --cacert … -h redis.bb.internal ping`.
- Env contract: `REDIS_URL=rediss://default:<pw>@redis.bb.internal:6379/0`

### `consul`

- Official `hashicorp/consul` image, single server in dev-like topology with persistence, HTTPS on 8501, plain HTTP disabled, TLS on RPC.
- Health check: the HTTPS API's leader endpoint with CA verification.
- Env contract:
  - `CONSUL_HTTP_ADDR=https://consul.bb.internal:8501`
  - `CONSUL_CACERT=<bundle path>`
- Service registration for apps follows the sidecar pattern in Phase 2 ([`starters.md`](starters.md)).
- ACLs are out of scope for v1.

## Later entries

### `s3-compatible` (Phase 2)

- Displayed as **S3 Compatible**: an object store speaking the S3 API, deliberately not branded as AWS S3. Implemented with the official SeaweedFS image (`weed server -s3`), single container. It replaces MinIO, whose community edition is archived and no longer publishes images.
- S3 API over HTTPS with the Breadboard certificate; credentials from generated secrets; buckets declared in `config` are created at startup.
- Health check: an authenticated S3 request over TLS against the FQDN.
- Env contract (generic names first, plus the variables official AWS SDKs read, so S3 clients work with zero code config):
  - `S3_ENDPOINT=https://s3.bb.internal:8333`, `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_FORCE_PATH_STYLE=true`
  - `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- Verify the image's exact tag and TLS configuration when the entry is built.

### Proxies and relays (Phase 5)

Traefik, Envoy, HAProxy, PgBouncer.
