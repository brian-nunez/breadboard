# Redis

## What you got

Redis with TLS only: the plaintext port is disabled (`port 0`) and clients connect to `rediss://redis.bb.internal:6379`. The default user has a generated password. Append-only persistence is on, so data survives a restart.

## Key concepts

- **`rediss://`.** The double s means TLS. Clients verify the server certificate against the trust bundle in `SSL_CERT_FILE`.
- **Cache-aside.** Read from Redis, fall back to the database on a miss, then fill the cache.
- **TTL.** Expiring keys bound how stale a cache can get.
- **Memory limit.** Redis keeps everything in memory. Without `maxmemory` and an eviction policy, it grows until the container limit kills it.

## Try this

- `SET` a key with `EX 10` and watch it disappear.
- Use `MULTI`/`EXEC` to update two keys atomically.
- Run `INFO memory` while you load data.

## Break this

- Fill Redis past its 256m container limit and watch the OOM kill in `bb status`.
- `docker restart bb-<experiment>-redis-1` and check that append-only persistence restored your keys.
- Point a client at port 6379 without TLS and read the error.
