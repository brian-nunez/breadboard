# PgBouncer

## What you got

PgBouncer 1.26 as a connection pool shared by every app that uses it, in front of one Postgres service (its single upstream). Apps that list it in `uses` receive the usual Postgres variables (`DATABASE_URL`, `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGSSLMODE`, `PGSSLROOTCERT`), pointing at `<name>.bb.internal:6432` with `sslmode=verify-full`, so the app checks the pool's certificate and name.

- **Apps to the pool:** TLS is required (`client_tls_sslmode = require`), with the pool's own certificate from the lab CA.
- **Pool to Postgres:** TLS with full verification (`server_tls_sslmode = verify-full`, the default `tls: {mode: on}`), or plaintext over a private network shared with Postgres when the upstream says `tls: {mode: off, reason: ...}`.
- **Credentials:** apps log in as the Postgres service's application role (`config.user`, its generated `password`), the same role apps of that Postgres get. PgBouncer writes its auth file from its environment when it starts, and reuses the password for its own server logins. No password is in experiment.yaml or in a generated file.

The generated config is at `.bb/runtime/config/<name>/pgbouncer.ini`, with `pgbouncer_hba.conf` next to it. `config.pool_mode` (default `transaction`) and `config.pool_size` (default 10) set how connections are lent and how many server connections the pool keeps.

## Key concepts

- **Client connections versus server connections.** Hundreds of app connections can share a few server connections. Postgres only ever sees PgBouncer's.
- **Pool modes.** `session` lends a server connection for a whole client session; `transaction` for one transaction, then returns it; `statement` for one statement. Transaction mode gives the most sharing but breaks session state such as `SET`, advisory locks and (without protocol-level support) prepared statements.
- **One pool per database and user.** Every app logs in as the same role here, so they all share one pool of `pool_size` server connections.
- **The admin console.** The virtual database `pgbouncer` answers `SHOW POOLS`, `SHOW STATS` and `SHOW SERVERS`. The HBA file admits it from inside the container only.

## Try this

- Open the admin console: `docker exec -it bb-<experiment>-<name>-1 sh -c 'PGPASSWORD="$BB_PG_PASSWORD" psql "host=127.0.0.1 port=6432 dbname=pgbouncer user=$BB_PG_USER sslmode=require"'`, then `SHOW POOLS;` while apps run queries: `cl_active` counts app connections, `sv_active` and `sv_idle` server connections.
- Ask Postgres who is connected: `select client_addr, ssl from pg_stat_activity join pg_stat_ssl using (pid)`. Every row is the pool's address, over TLS.
- Set `config.pool_size: 2` and run ten slow queries at once: eight wait (`cl_waiting`) and `maxwait` grows.
- Switch `config.pool_mode` to `session` and watch each app connection hold a server connection for its whole life.

## Break this

- Run `SET statement_timeout = '1s'` in transaction mode, then a slow query in a new transaction: it may run on another server connection, without your setting.
- Connect with `sslmode=disable`: the pool refuses, because TLS is required.
- Try the admin console from an app container: the HBA file rejects it.
- Stop Postgres and query through the pool: clients wait, then fail with a server login error, and recover once Postgres is back.
