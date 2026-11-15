# Consul

## What you got

One Consul server with its own Raft quorum of one, persisted to a volume. The HTTP API is HTTPS only at `https://consul.bb.internal:8501`; plain HTTP is disabled. Apps get `CONSUL_HTTP_ADDR` and `CONSUL_CACERT`, which the official client reads with no code.

## Key concepts

- **Catalog.** Services register with a name, address, port, and health checks; others look them up by name.
- **Health checks.** Consul only returns instances whose checks pass, so lookups skip broken instances.
- **KV store.** A strongly consistent key/value store for configuration and coordination.
- **Raft.** Writes go through the leader. One server means no fault tolerance, which is fine for a lab.

## Try this

- Register a service from an app with the agent API and look it up with `Health().Service(name, "", true, nil)`.
- Store a config value in KV and watch it with a blocking query.
- Register two instances and stop one; watch it leave the healthy list.

## Break this

- `docker stop bb-<experiment>-consul-1` and see what your app does when discovery is gone.
- Register a service with a failing check and confirm lookups skip it.
