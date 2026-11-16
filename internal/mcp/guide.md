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
- Breadboard runs its own CA (`ca`). Every service and app gets a certificate for its name, mounted at `/etc/bb/tls/tls.crt` and `/etc/bb/tls/tls.key`. The CA is at `/etc/bb/ca.pem`, and `SSL_CERT_FILE` points at a trust bundle (`/etc/bb/trust-bundle.pem`) holding it.
- Connections use TLS unless an app's `uses` entry turns it off with `tls: {mode: off, reason: ...}` (only toward services with a plaintext listener).

## How apps connect

An app lists the services it needs in `uses`. For each one it receives that service's environment contract, for example `DATABASE_URL` and `PG*` for Postgres, `KAFKA_BROKERS` and `KAFKA_SECURITY_PROTOCOL` for Kafka, `REDIS_URL` for Redis, `CONSUL_HTTP_ADDR` for Consul, and `AWS_*` with `S3_ENDPOINT` for S3. Every app also gets `TLS_CERT_FILE`, `TLS_KEY_FILE`, `BB_SERVICE` and `BB_FQDN`. `catalog` lists the variable names per type; `creds` shows their values; `service_details` shows a service's whole setup.

Registering an app in Consul: add `register: {port: <tls port>}` to the app and Consul to its `uses`; bb adds a sidecar agent (`<app>-agent`) that registers it, so the app code does not register itself.

## Usual workflow

1. `init` creates the experiment (or restores `.bb/` in a fresh clone).
2. `add_service` adds infra, or `apply_starter` adds a working example with its infra.
3. `up` builds apps, starts everything and waits until every service is healthy and stays healthy. It fails if a service is unhealthy or restarts while starting.
4. Inspect: `status` (state, health, CPU, memory), `logs`, `creds`, `service_details`, and the explorers: `postgres_tables`, `redis_keys`, `consul_catalog`, `kafka_topics`, `s3_buckets`.
5. `down` stops it; `down` with `volumes` also deletes the data.

Editing `experiment.yaml` or app code takes effect on the next `up`.

## Debugging

- `up` failed: read its error (it lists the progress so far), then `logs` for the failing service, and `status` for restarts or `oom_killed`.
- An app cannot connect: compare its environment (`service_details` for the app, `creds` for values) with what the code reads; check the edge in `status` (`tls` and port).
- Data questions: the explorers read live data with each service's own CLI inside its container.
- `experiment.yaml is invalid`: every violation is listed with its YAML path.
- Another bb command running on the same experiment makes a tool fail with "busy"; retry once it finishes.

## Limits of this server

- Tools act on the experiment containing the directory `bb mcp` was started in. `dir` may name another directory inside it, never one outside.
- `logs` returns recent lines; it does not follow.
- Credentials are local, generated per experiment, and only valid inside it.
- `bb mcp --read-only` exposes only the tools that change nothing the user owns.
