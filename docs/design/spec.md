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
| `apps.<name>.path` | yes | Relative to the spec file. Must contain a `Dockerfile` with a `dev` stage (checked by `bb up` and `bb export`). Compile the app in that stage and run the binary as the `command`, as the starters do; then the app is the container's main process and receives SIGTERM. |
| `apps.<name>.command` | no | Overrides the image command. |
| `apps.<name>.uses` | no | Infra services this app connects to. Each entry creates an edge, injects that service's env contract, and adds `depends_on: service_healthy`. When two used services share an env variable name (two Postgres services, say), the later one's variables are prefixed with its name: `ANALYTICS_DATABASE_URL`. |
| `apps.<name>.ports` | no | Container ports to publish on `127.0.0.1`. |
| `apps.<name>.health` | no | Opt into an HTTP health check (`path`, default `/healthz`; `port`, default 8080; the port is injected as `BB_HEALTH_PORT`). Without it, the check is that the container's main process is running, so app code needs no health endpoint. |
| `apps.<name>.register` | no | Register the app in a service registry (Consul) through a sidecar agent, so app code does not register itself. `port` (required): the port the app serves TLS on. `name`: the registered service name, default the app's name. `check`: a path for an HTTPS health check; without it the check is a TCP connection that completes a TLS handshake. Requires a registry service in `uses`; the sidecar is a service named `<app>-agent`, which takes that name in the namespace. |
| `apps.<name>.resources` | no | As for services. Default `256m` and `0.5` cpus: room for a running Go app. Compiling happens while the image builds, outside this limit. An app that instead runs `go run` at start compiles under the limit and needs much more (`github.com/redis/go-redis/v9` alone takes over 1 GiB). |

Service and app names share one namespace and must be unique DNS labels.

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

Reserved for later phases, rejected by v1 validation: `tls.clientAuth` (mTLS), `tls.ca` (multiple CAs), infra-to-infra `upstream` edges for proxies.

## Validation rules

Validation fails with every violation listed, each with its YAML path:

- Unknown top-level or nested keys are errors (catches typos).
- `type` must exist in the catalog; `config` must satisfy the entry's schema.
- Every `uses` target must exist and be an infra service; no target may be listed twice.
- `tls.mode` is `on` or `off`; `off` is allowed only toward entries with a plaintext listener.
- `tls.mode: off` requires a non-empty `reason`.
- Reserved fields are rejected with a message naming the phase that introduces them.
