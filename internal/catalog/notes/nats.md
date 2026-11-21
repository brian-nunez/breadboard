## What you got

A single-node NATS 2.15 server (`nats:2.15.3-alpine3.22`) with TLS required on port `4222`, token authentication, loopback-only monitoring, and JetStream persistence enabled by default (`config.jetstream: true`):

- **TLS client listener on `4222`** using the service's lab certificate (`/etc/bb/tls/tls.crt`, `/etc/bb/tls/tls.key`) verified against `/etc/bb/ca.pem` (`SSL_CERT_FILE`).
- **Token authentication** generated in `.bb/secrets.json` and passed to `nats-server` via `--auth "$NATS_TOKEN"` (never written into `nats.conf`).
- **JetStream persistence** stored in a named volume at `/data` (`max_memory_store: 64MB`, `max_file_store: 512MB`). Set `config.jetstream: false` (or `bb add nats --config jetstream=false`) to practice pure Core NATS without persistence.
- **Loopback monitoring (`127.0.0.1:8222`)** unreachable from other containers, backing the health check and Breadboard's read-only explorer.
- **Tiny footprint:** `128m` memory and `0.5` CPU, starting in well under a second.

## Key concepts

- **Core NATS (at-most-once):** Subjects are hierarchical dot-separated strings (`pricing.quote.us`) with single-token (`*`) and multi-token (`>`) wildcards. Messages are delivered only to currently connected subscribers; if nobody is listening, the message is dropped.
- **Queue groups:** Multiple subscribers sharing a queue group name automatically load-balance messages across replicas with zero broker-side configuration.
- **Request-reply:** A built-in RPC pattern where the requester subscribes to a unique ephemeral `_INBOX.*` reply subject and publishes a request with that reply-to address.
- **JetStream streams & consumers:** Opt-in durability on top of Core NATS. A `WorkQueuePolicy` stream deletes each message once a consumer acknowledges it; durable Pull Consumers let worker replicas pull batches on demand, explicitly `Ack()`, or `NakWithDelay()` for retries.
- **JetStream Key-Value:** Built on top of a compacted JetStream stream (`KV_<bucket>`), offering versioned keys, optimistic concurrency (`Update` with last revision), and live `Watch` streams without polling.

## Try this

- Inspect live server stats and JetStream streams from inside the container:

  ```sh
  docker exec -it <container> wget -qO- 'http://127.0.0.1:8222/varz'
  docker exec -it <container> wget -qO- 'http://127.0.0.1:8222/jsz?streams=true&consumers=true'
  ```

- Add a pure Core NATS service without JetStream:

  ```sh
  bb add nats --config jetstream=false
  ```

## Break this

- **Drop a Core NATS subscriber:** Stop a subscriber in `nats/core-pubsub-request-reply` while the requester keeps publishing — messages sent while it is down are gone forever (at-most-once), whereas requests sent to a 2-replica queue group seamlessly shift to the surviving replica.
- **Stop a JetStream worker mid-batch:** In `nats/jetstream-workqueue`, kill one worker replica and watch unacknowledged messages exceed `AckWait` and redeliver to the remaining replica without losing a single job.
- **Send a stale KV revision:** In `nats/jetstream-kv-watch`, watch `kv.Update` reject a write whose expected revision is out of date (`ErrKeyExists` / wrong last sequence).
