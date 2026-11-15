# Postgres

## What you got

PostgreSQL with TLS required on every network connection. Apps connect as the role in `config.user` (default `app`) to the database in `config.database` (default `app`), with `sslmode=verify-full`, so the client checks both the certificate chain and the hostname `postgres.bb.internal`. The superuser password is generated too, but apps never use it.

The role and database are created on the first start, when the data volume is empty. After changing `config.user` or `config.database`, run `bb down --volumes` so the next `bb up` creates them.

## Key concepts

- **`sslmode=verify-full`.** The only mode that defends against a man in the middle: `require` encrypts but trusts any certificate.
- **`pg_hba.conf`.** Postgres decides who may connect, from where, and how, line by line. Breadboard allows only `hostssl` lines, plus a `hostnossl` line limited to a private segment when an edge asks for plaintext.
- **Key file permissions.** Postgres refuses a private key that others can read or that another user owns. Breadboard copies the mounted key with mode 0600 and the postgres owner before the server starts.
- **Roles.** Apps use a least-privilege role that owns its database, not the superuser.

## Try this

- Connect with `psql "$DATABASE_URL"` from an app container and run `select ssl, version, cipher from pg_stat_ssl join pg_stat_activity using (pid);`.
- Change `sslmode` to `disable` in an app and watch the server reject it.
- Run a migration from your app at startup and make it idempotent.

## Break this

- Exhaust connections: open more than `max_connections` (100) from a loop and read the error.
- `docker stop bb-<experiment>-postgres-1` mid-transaction and check what your app does with the broken connection.
- Set `resources.memory: 128m` and run a large sort.
