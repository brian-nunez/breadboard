# HAProxy

## What you got

HAProxy 3.4 in front of the upstreams you list under `services.<name>.upstreams`. Clients connect to `https://<name>.bb.internal:8443` (your app receives it as `<NAME>_URL`) and HAProxy presents its own certificate from the lab CA. With one upstream every request goes there; with several, `/<route>/` reaches each one with the prefix stripped, and the first upstream also takes everything else. Each upstream hop has its own TLS mode:

- **Re-encrypt** (`tls: {mode: on}`, the default): HAProxy decrypts, then opens a new TLS connection to the backend and verifies its certificate against the lab CA, with SNI set to the backend's name.
- **Terminate** (`tls: {mode: off, reason: ...}`): HAProxy decrypts and sends plaintext HTTP to the backend over a private network shared only with it. An app behind a terminating hop binds `BB_PLAIN_LISTEN_ADDR`, so nothing else can reach its plaintext port.
- **Passthrough** (`tls: {mode: passthrough}`): HAProxy never decrypts. It reads the server name (SNI) from the client's TLS hello on port 9443 (`<NAME>_PASSTHROUGH_ADDR`) and forwards the bytes, so the client sees the backend's own certificate.

The generated config is at `.bb/runtime/config/<name>/haproxy.cfg`, commented for copying. Every app replica is found through Docker's DNS with `server-template`, balanced round robin, and checked every 2 seconds.

## Key concepts

- **Frontend and backend.** A frontend accepts connections (`bind`) and picks a backend (`use_backend`, `default_backend`); a backend holds servers and a balancing algorithm.
- **`mode http` versus `mode tcp`.** In HTTP mode HAProxy parses requests, so it can route on paths and set headers. In TCP mode it only sees bytes, which is why passthrough routes on SNI and cannot look at paths.
- **`server-template` and `resolvers`.** One line creates N server slots filled from the DNS answers for one name, re-resolved at runtime. Scale the app and HAProxy follows.
- **Active health checks.** `check` connects to each server (with a TLS handshake for re-encrypt). A server that fails three checks in a row leaves the rotation; two passing checks bring it back.
- **`X-Forwarded-Proto` and `X-Forwarded-For`.** After termination the backend no longer sees TLS or the client's address; these headers tell it.
- **`maxconn` and `nbthread`.** The generated `global` caps concurrent connections at 256 on one thread. Without `maxconn`, HAProxy sizes its connection tables from the file-descriptor limit, which inside Docker is often a million: about 90 MiB of memory before a single request. To load-test, copy the config (`file:`), raise `maxconn` (and `nbthread`), and give the service more memory with `resources`.

## Try this

- Open the stats page from inside the container: `docker exec bb-<experiment>-<name>-1 bash -c "exec 3<>/dev/tcp/127.0.0.1/8404; printf 'GET /stats;csv HTTP/1.0\r\n\r\n' >&3; cat <&3"` and watch `scur` (current sessions) per server.
- Give an app `replicas: 3` and call `$<NAME>_URL` thirty times: the answering instance rotates.
- Add a second upstream and reach it at `/<route>/`; the first upstream keeps every other path.
- Switch `balance roundrobin` to `leastconn` or `source` in your own copy (`file:`) and compare.
- Connect to `$<NAME>_PASSTHROUGH_ADDR` with `openssl s_client -servername <backend>.bb.internal` and read whose certificate you get.

## Break this

- Stop one replica (`docker stop bb-<experiment>-<app>-2-1`) and watch its server go `DOWN` in the stats page while requests keep succeeding.
- Point a re-encrypt upstream at an app that serves a certificate for another name: the handshake fails verification and the server stays down.
- Try to reach a terminated app's plaintext port from any container but HAProxy: it is not on that network.
- Send passthrough traffic with an SNI no upstream claims: HAProxy closes the connection.
- In your own copy, delete `maxconn 256` from `global` and restart: the log says `Automatically setting global.maxconn to …`, and `docker stats` shows the memory it reserves for that many connections.
