# Security: PKI, TLS, secrets

Breadboard is secure by default and insecure only by intent. This document is the single source for how certificates, TLS, network segments, and credentials work.

## Domain and naming

- Install-level setting `domain`, default `bb.internal`, stored in `<BB_HOME>/config.yaml` and overridable by `BB_DOMAIN`. `.internal` is reserved by ICANN for private use and never resolves publicly. The domain must end in a reserved special-use suffix (`.internal`, `.test`, `.localhost`, `.home.arpa`), so the CA's name constraint can never cover a real site.
- Every infra and app service gets the Docker network alias `<service>.<domain>` (e.g. `postgres.bb.internal`).
- Connection strings, health checks, and certificates always use the FQDN. Bare names like `postgres` still resolve in Docker but are not covered by certificates.
- Changing the domain mints a new CA (the name constraint is bound to the domain) and reissues all leaf certificates on the next `bb up`.

## Certificate authority

- v1 has exactly **one CA per install**, named `root`, created on first use with Go's standard library (`crypto/x509`, ECDSA P-256).
- The CA is **name-constrained** (critical) to the domain: permitted DNS domain `.<domain>` and permitted URI domain `<domain>`. A leaked CA key cannot mint a certificate a client would accept for any real site.
- The CA lives in `<BB_HOME>/ca/` (`root.key` at `0600`, directory `0700`). Future CAs sit beside it, one file pair per CA name. It is never added to the host trust store automatically; `bb ca` prints instructions for anyone who opts in.
- Lifetimes: CA 5 years; leaf certificates 90 days, reissued on `bb up` when fewer than 30 days remain.

## Leaf certificates

One per service (infra and app), issued at `bb up`:

- DNS SANs: `<service>.<domain>`; `<service>.<experiment>.<domain>` reserved for host-side access through a proxy (Phase 5).
- URI SAN: the service **identity** `spiffe://<domain>/experiment/<exp>/service/<svc>`.
- Extended key usage: **both** `serverAuth` and `clientAuth`. App services get certificates in v1 even though nothing checks them yet, so mTLS later is a policy change, not a reissue.

## Trust distribution

Every container receives the trust bundle (Breadboard CA followed by the host's public roots, from the same PEM bundle paths Go reads on macOS and Linux) at `/etc/bb/trust-bundle.pem`, the CA alone at `/etc/bb/ca.pem`, and its own certificate and key under `/etc/bb/tls/`, each file mounted read-only.

Inside `.bb/runtime/` (and an export's `tls/`), key files are mode `0644` so non-root users in official images can read them through the bind mount; the directories holding them are `0700`, so no other host user can reach them. The canonical keys in `.bb/certs/` stay `0600`.

- `SSL_CERT_FILE` points at the bundle. Go's TLS stack reads it, so `pgx`, `go-redis`, `franz-go`, and `hashicorp/consul/api` verify Breadboard certs with no code changes, and outbound internet TLS keeps working.
- `TLS_CERT_FILE` and `TLS_KEY_FILE` point every app at its own certificate and key, so an app that serves TLS to other apps (for example `http.ListenAndServeTLS`) names no paths in code.
- Service-specific trust variables (e.g. `CONSUL_CACERT`) come from each catalog entry's env contract.
- Non-Go starters (later) add their ecosystem's variable (`NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, …) in the starter definition.

## TLS policy: listeners, edges, segments

TLS is decided per **edge**, because proxies and relays make per-hop decisions.

- A catalog entry declares its **listeners**. Each has a protocol, port, and TLS mode.
- Every `uses` entry creates an **edge** from the client to one of the target's listeners.
- **Default: every edge is TLS with full verification** (`verify-full` or equivalent). The user writes nothing to get it.
- **Plaintext is intentional:** `tls.mode: off` on an edge requires a `reason` (enforced by validation). The engine then:
  1. enables a plaintext listener on the target, used only by edges that asked for it;
  2. places that listener on a private **segment** (compose network) shared only with those clients, so nothing else can reach the unencrypted port:
     - the segment is an `internal` compose network named `plain-<target>`, with a `/24` subnet allocated from `10.213.0.0/16` (skipping subnets other Docker networks use) and recorded in `.bb/state.json` so it stays stable;
     - the target gets the static address `.10` on the segment and the alias `<target>.plain.<domain>`; clients on a plaintext edge connect to that alias;
     - the entry binds its plaintext listener to that address (Kafka, Consul) or accepts unencrypted connections only from that subnet (Postgres `hostnossl`). Entries that cannot do either (Redis) offer no plaintext listener;
  3. reports the edge as a warning in `bb status`, draws it dashed red in the UI, and writes the reason as a comment in exported compose files.
- Loopback inside one container (an app's opt-in `/healthz` checked by its health check) is not an edge and is exempt.

### Proxy and relay modes (Phase 5)

Proxies are infra services with `upstream` edges to other infra services. Each upstream edge carries its own TLS policy, which yields the standard patterns:

| Pattern | Client → proxy | Proxy → backend |
|---|---|---|
| Terminate | TLS | `off` with reason; backend on a private segment |
| Passthrough | TLS, routed by SNI without decrypting | same TLS session, backend's own cert |
| Re-encrypt | TLS | TLS; proxy verifies backend against the CA |

## Extension points for mTLS and multiple CAs

These are designed into v1 so later phases add policy, not rework:

| Extension | v1 shape | Later |
|---|---|---|
| Client authentication | Every leaf has `clientAuth` EKU and a SPIFFE identity. Edge `tls.clientAuth` is reserved; v1 only allows `none`. | `request` / `require`; per-service authorization by identity (Kafka ACLs, Postgres `cert` auth, Consul intentions). |
| Multiple CAs | CAs are addressed by name (`root` in v1); `.bb/state.json` records which CA issued each leaf; trust bundles are built from a list of CA names. | Per-experiment or per-service CAs, intermediates, cross-signing, and rotation drills via edge `tls.ca`. |
| Identity | SPIFFE URI in every leaf. | Authorization rules keyed by identity; export to SPIRE / service-mesh identities. |

## Secrets

- Generated by the engine with `crypto/rand` on first `bb up`: passwords, tokens, and similar. Stored in the project's `.bb/secrets.json` (`0600`), which is never committed.
- Users inspect them with `bb creds`; they never need to type one.
- Secrets outlive their service: removing a service from `experiment.yaml` keeps its secrets, because its data volume survives until `bb down --volumes`, and a re-added service must match the data. Deleting `.bb/` resets them.
- **Materializers** render secrets into a medium. Runtime and compose export support:
  - `inline` (default): explicit `environment:` values in the compose file.
  - `dotenv`: a `.env` (mode `0600`) beside the compose file, referenced as `${<SERVICE>__<KEY>}` (for example `${POSTGRES__PASSWORD}`); the export adds `.env` to a generated `.gitignore`.
- Exporters add their own materializers later: Kubernetes Secrets, Terraform sensitive variables and cloud secret managers, Ansible Vault ([`exporters.md`](exporters.md)).
- Secrets never appear in `experiment.yaml`, logs, or error messages.

## Out of scope for v1

- Encrypting `.bb/` and the CA key at rest (file permissions only).
- Kafka authentication (arrives with mTLS or SASL).
- Consul ACLs.
