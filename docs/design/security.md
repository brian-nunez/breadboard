# Security: PKI, TLS, secrets

Breadboard is secure by default and insecure only by intent. This document is the single source for how certificates, TLS, network segments, and credentials work.

## Domain and naming

- Install-level setting `domain`, default `bb.internal`, stored in `<BB_HOME>/config.yaml` and overridable by `BB_DOMAIN`. `.internal` is reserved by ICANN for private use and never resolves publicly. The domain must end in a reserved special-use suffix (`.internal`, `.test`, `.localhost`, `.home.arpa`), so the CA's name constraint can never cover a real site.
- Every infra and app service gets the Docker network alias `<service>.<domain>` (e.g. `postgres.bb.internal`). An app with replicas runs instances `<app>-<n>`, each with its own alias `<app>-<n>.<domain>` plus the shared alias `<app>.<domain>`, which resolves to every instance.
- Connection strings, health checks, and certificates always use the FQDN. Bare names like `postgres` still resolve in Docker but are not covered by certificates.
- Changing the domain mints a new CA (the name constraint is bound to the domain) and reissues all leaf certificates on the next `bb up`.

## Certificate authority

- Lab traffic has exactly **one CA per install**, named `root`, created on first use with Go's standard library (`crypto/x509`, ECDSA P-256).
- The CA is **name-constrained** (critical) to the domain: permitted DNS domain `.<domain>` and permitted URI domain `<domain>`. A leaked CA key cannot mint a certificate a client would accept for any real site.
- The CA lives in `<BB_HOME>/ca/` (`root.key` at `0600`, directory `0700`). Future CAs sit beside it, one file pair per CA name. It is never added to the host trust store automatically; `bb ca` prints instructions for anyone who opts in.
- A second CA, `host`, signs only the host router's certificate for `*.localhost` names (see [Host access](#host-access-phase-5-wave-e)); it never signs a lab certificate.
- Lifetimes: CA 5 years; leaf certificates 90 days, reissued on `bb up` when fewer than 30 days remain.

## Leaf certificates

One per service (infra and app), issued at `bb up`:

- DNS SANs: `<service>.<domain>`. Host-side names are `*.localhost` names on the host router's certificate, never on a lab certificate (ADR-0017). A replica's certificate names both `<app>-<n>.<domain>` and `<app>.<domain>`, so a client verifies it by either name.
- Replicas of one app share its identity (`.../service/<app>`): authorization by identity (Phase 6) treats them as one workload.
- Sidecars have no certificate of their own: they present their app instance's certificate, as the address they listen on is the instance's.
- URI SAN: the service **identity** `spiffe://<domain>/experiment/<exp>/service/<svc>`.
- Extended key usage: **both** `serverAuth` and `clientAuth`. App services get certificates in v1 even though nothing checks them yet, so mTLS later is a policy change, not a reissue.

## Trust distribution

Every container receives the trust bundle (Breadboard CA followed by the host's public roots, from the same PEM bundle paths Go reads on macOS and Linux) at `/etc/bb/trust-bundle.pem`, the CA alone at `/etc/bb/ca.pem`, and its own certificate and key under `/etc/bb/tls/`, each file mounted read-only.

Inside `.bb/runtime/` (and an export's `tls/`), key files are mode `0644` so non-root users in official images can read them through the bind mount; the directories holding them are `0700`, so no other host user can reach them. The canonical keys in `.bb/certs/` stay `0600`.

- `SSL_CERT_FILE` points at the bundle. Go's TLS stack reads it, so `pgx`, `go-redis`, `franz-go`, and `hashicorp/consul/api` verify Breadboard certs with no code changes, and outbound internet TLS keeps working.
- `BB_FQDN` is the app's own address, the name on its certificate, for apps that tell others where to reach them (for example when registering in Consul).
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
     - the segment is an `internal` compose network named `plain-<target>`, with a `/24` subnet allocated from `10.213.0.0/16` (skipping subnets other Docker networks use; each lab starts its search at a block hashed from its name, D-202) and recorded in `.bb/state.json` so it stays stable;
     - the target gets the static address `.10` on the segment and the alias `<target>.plain.<domain>`; clients on a plaintext edge connect to that alias;
     - the entry binds its plaintext listener to that address (Kafka, Consul) or accepts unencrypted connections only from that subnet (Postgres `hostnossl`). Entries that cannot do either (Redis) offer no plaintext listener;
  3. reports the edge as a warning in `bb status`, draws it dashed red in the UI, and writes the reason as a comment in exported compose files.
- Loopback inside one network namespace is not an edge and is exempt: an app's opt-in `/healthz` checked by its health check, and traffic between an app and its own sidecars, which share the pod's namespace and never leave it (D-076). An Envoy sidecar may receive plaintext from its app on `127.0.0.1` and forward it as TLS, or terminate TLS and hand the app plaintext on loopback; the app then binds `127.0.0.1` only (`BB_LISTEN_ADDR`), so nothing can reach it around the sidecar. Every hop between containers stays TLS with full verification: Envoy verifies targets against the lab CA with SNI set to the target's name, and PgBouncer connects to Postgres with `verify-full`.

### Transparent mode (Phase 5)

An Envoy sidecar with `transparent` captures all of its app's TCP traffic, so the app calls `http://<target>.<domain>:<port>` in plain HTTP and the wire is TLS:

- The rules are installed by `<app>-netinit`, a one-shot container in the pod built from a generated Dockerfile (`FROM alpine:3.24.4`, `apk add --no-cache iptables`). It is the **only** container granted `NET_ADMIN`; the app and the long-running Envoy never hold it. It runs with `restart: "no"`, and the sidecars and app start only after it completed successfully, so no packet leaves before the rules exist. It prints the rules it installed (`iptables -t nat -S`).
- Outbound: `nat OUTPUT` sends TCP to a chain `BB_OUT` that returns for the reserved users and for `127.0.0.0/8`, and redirects everything else to Envoy on 15001. UDP, including DNS to Docker's resolver, is untouched. The script is idempotent (D-090).
- Reserved users (D-096): in a transparent pod Envoy runs as uid **1337** and a registration agent as uid **1338**; only these are exempt from redirection (not the images' default users). Before any container starts, `bb up` builds each transparent app's image, resolves the user it starts as (`Config.User`; a name through `id -u` inside the image) and refuses to start if it is 1337 or 1338, or if a named user cannot be resolved (use a numeric `USER`). The app also runs with `no-new-privileges` and without `SETUID`/`SETGID`, so a process in it cannot become a reserved user later.
- Capture mode is part of a pod's identity (D-102): the rules live in the holder's namespace, so switching transparent mode on or off for a pod recreates its holder and every container in it; no stale rules survive.
- Inbound: only declared external listeners accept connections into the pod. Envoy's `inbound.port` is reached directly, so the TLS handshake is Envoy's; the app's own TLS port (`register.port`, when it is not Envoy's `inbound.port`) is redirected to Envoy's 15006, which hands the still-encrypted bytes to the app on `127.0.0.1`; a registration agent's gossip port 8301 is reached directly. Every other TCP connection into the pod is reset (`filter INPUT`), so the app's plaintext port, Envoy's admin API, outbound listeners, PgBouncer and Fluent Bit are unreachable from other containers. In a transparent pod a published port reaches only a declared listener.
- Declared outbound targets get TLS origination with CA verification: a plain HTTP connection to a target's TLS port is routed by its `Host` header. On `egress: allow` (default) anything else passes through untouched to its original destination. On `egress: deny` destinations are allowed by port **and address** (D-091): a lab with a transparent pod has a fixed subnet and fixed container addresses, recorded in `.bb/state.json`. A declared target's TLS port at its replicas' addresses accepts plain HTTP (another host gets `403`) and the app's own TLS; each service in `uses` is reachable on its port at its address; everything else, including an undeclared host on an allowed port, is closed by Envoy's RBAC filter (counted as `egress_denied.rbac.denied`).
- With Envoy stopped, the app reaches nothing: there is no bypass.

### Proxy and relay modes (Phase 5)

Proxies are infra services (catalog entries marked as proxies) with **upstream** edges to apps (every replica) and to other infra services, declared in `services.<name>.upstreams` ([`spec.md`](spec.md#proxies-and-upstreams)). Clients always reach a proxy over TLS: an HTTP proxy serves its own certificate on `8443`, and on `9443` routes passthrough TLS by SNI. Each upstream edge carries its own TLS mode:

| Mode (`tls.mode`) | Client → proxy | Proxy → backend |
|---|---|---|
| `on`, re-encrypt (default) | TLS with the proxy's certificate | TLS; the proxy verifies the backend against the lab CA with SNI and the verified name set to the backend's shared name `<target>.<domain>` (on every replica's certificate) |
| `off`, terminate | TLS with the proxy's certificate | plaintext, with a required `reason`, on the private segment `plain-<target>` shared only by the target and the proxies that asked for plaintext |
| `passthrough` | TLS, routed by SNI without decrypting | the same TLS session; the client verifies the backend's own certificate |

- **Plaintext hops to infra** use the target's existing plaintext listener and segment, exactly as a `tls.mode: off` `uses` edge does; the proxy is one more client on `plain-<target>`.
- **Plaintext hops to apps.** The app (every replica) joins `plain-<app>`, an `internal` network with a `/24` from the same allocator and `.bb/state.json` record as other segments. Replica `n` gets the static address `.10 + (n-1)` (a single instance `.10`), the alias `<app>.plain.<domain>` and its own `<instance>.plain.<domain>`; Docker hands out only the upper half (`ip_range`, `.128/25`) to the proxies on it, so a replica added later always finds its address free (D-122). The app receives `BB_PLAIN_LISTEN_ADDR=<its segment address>:<port>` to bind its plaintext listener there.
- **Confinement is enforced, not voluntary** (D-121). An app that is the target of a terminate hop runs as a pod (holder `<instance>-pod`), and its init container `<instance>-netinit` (built like transparent mode's, `FROM alpine:3.24.4` with iptables, the only container holding `NET_ADMIN`) installs a filter `INPUT` chain `BB_PLAIN` that drops TCP to the plaintext port unless it is addressed to the instance's segment address. It runs before the app starts (`service_completed_successfully`), and the app holds no `NET_ADMIN`, so it cannot remove the rule. An app that ignores `BB_PLAIN_LISTEN_ADDR` and binds every interface is still reachable on that port only through the segment, which holds only the app and the terminating proxies. The pod's `breadboard.capture` label is `guard`, so switching the guard on or off recreates the namespace. An app in transparent mode cannot be a terminate target (its pod admits only declared TLS listeners).
- Every plaintext hop is reported like other plaintext edges: a warning in `bb up` and `bb status` (`plaintext edge <proxy> -> <target>:<port>`) and the reason on the segment in the compose file. (The UI draws proxy edges, dashed red for terminate, once the architecture graph learns them; wave B.)
- A proxy's admin, stats or monitor endpoint binds to `127.0.0.1` only; explorers read it through `docker exec` (ADR-0012). PgBouncer's admin console shares its client port, so its HBA file admits the `pgbouncer` database from `127.0.0.1` only (D-120).
- User text that reaches a generated config (a `tls.reason`) is rendered as comment lines through `catalog.Comment`, so a multi-line reason cannot become a directive (D-123).
- **The PgBouncer pool** requires TLS from apps (`client_tls_sslmode = require`; apps use `verify-full` against the pool's name) and, in mode `on`, connects to Postgres with `verify-full`. Apps log in with the Postgres service's application role; the password reaches the pool only through its environment and a `0600` auth file written at start, never a generated file.
- A user-owned `file` replaces only the proxy's config; its certificate, the CA and the trust bundle stay mounted by Breadboard.
- A Traefik `static_file` (D-207) has the same power over the proxy as `file`: a learner can weaken their own lab's TLS with it, for example a global `serversTransport` with `insecureSkipVerify: true`, which turns off backend verification for every re-encrypt hop that does not set its own transport. Breadboard's listeners, certificate and providers stay its own, because `entryPoints`, `providers`, `api`, `ping` and `global` are refused (case-insensitively, and through a merge key, a tagged key such as `!!binary` or an alias: only plain string keys are accepted): the static_file cannot add a listener, publish the API off loopback, add the Docker provider or turn on the version check and usage statistics. Sections that send data (a tracing or metrics exporter, plugins fetched from the plugin catalog) reach whatever endpoint the learner names, like any app in the lab; Breadboard generates none of them.
- No proxy gets the Docker socket. Traefik is configured by its file provider only, so a proxy compromised through its routing config cannot reach the Docker API.
- In the generated Envoy and Traefik configs, text from `experiment.yaml` (an upstream's `reason`) appears only inside comments, and line breaks or control characters in it cannot end the comment (D-114).

## Host access (Phase 5 wave E)

Apps and infra HTTPS services marked `host` are reachable from the host at `https://<target>.<experiment>.localhost:<port>` ([ADR-0017](../adr/README.md)):

- **Host CA.** The install's second CA, `host`, in `<BB_HOME>/ca/` (`host.key` at `0600`, `host.crt` at `0644`, like `root`), ECDSA P-256, 5 years. Name-constrained (critical) to the permitted DNS domain `.localhost` only, with every IP range excluded and the URI and email name types closed: the same subtree, `.invalid`, is both permitted and excluded for each, so any URI or email address is outside the permitted subtree or inside the excluded one (D-195). A leaked key can sign neither a real site, an IP address, an email address, nor a lab identity, in Go and OpenSSL alike (an empty exclusion alone holds in Go but not in OpenSSL). Created by `bb ca` or the first `bb up` that needs it; never added to a trust store automatically. `bb ca` prints it and how to trust it (macOS keychain, Linux trust anchors, Firefox's own store).
- **Host certificate.** One per lab, for the router: DNS SANs exactly the lab's host names (`<target>.<experiment>.localhost`, plus `<app>-<n>.<experiment>.localhost` for each replica), no URI SAN, `serverAuth` only, 90 days, reissued below 30 days or when the names change. Stored in `.bb/certs/bb-host/` (`host.key` at `0600`, `host.crt` at `0644` like every lab certificate) and mounted read-only only in the router, at `/etc/bb/host/`; removed when the last `host` target goes (D-199).
- **Router.** `bb-host` (Traefik, file provider only, no Docker socket) is published on `127.0.0.1:<port>` only and joins only the lab's default network. It terminates the host's TLS with the host certificate (TLS 1.2 minimum) and re-encrypts to every replica of the target, verifying it against the lab CA with the server name set to the target's shared name (or the instance's name for an instance route). There is no plaintext hop and no passthrough: the host never sees a lab certificate, and lab clients never see the host CA.
- **Scope.** Only HTTP over TLS. TCP services are not routed.
- **Exposure.** A host name is reachable by any program on the host, including web pages in a browser, exactly as a published port is; it is opt-in per target for that reason. Cross-site requests from a web page still reach the app, so an app that changes state on a request should check `Origin` as it would in production.

## Extension points for mTLS and multiple CAs

These are designed into v1 so later phases add policy, not rework:

| Extension | v1 shape | Later |
|---|---|---|
| Client authentication | Every leaf has `clientAuth` EKU and a SPIFFE identity. Edge `tls.clientAuth` is reserved; v1 only allows `none`. | `request` / `require`; per-service authorization by identity (Kafka ACLs, Postgres `cert` auth, Consul intentions). |
| Multiple CAs | CAs are addressed by name (`root` for lab traffic; `host` for the host router since wave E); `.bb/state.json` records which CA issued each leaf; trust bundles are built from a list of CA names. | Per-experiment or per-service CAs, intermediates, cross-signing, and rotation drills via edge `tls.ca`. |
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
- Who sees them: `bb creds` prints them, and the MCP `creds` tool returns them so an agent can debug connections (D-071); they then reach the agent's model provider, which is acceptable because they are generated per experiment and valid only on its local network. The web UI never shows them (D-065).

## Out of scope for v1

- Encrypting `.bb/` and the CA key at rest (file permissions only).
- Kafka authentication (arrives with mTLS or SASL).
- Consul ACLs.
