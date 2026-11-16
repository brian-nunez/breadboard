# Envoy

## What you got

Envoy 1.40 as a standalone proxy in front of the upstreams you list under `services.<name>.upstreams`. (This is the infra `envoy` type; the Envoy that runs beside an app is the `envoy` sidecar.) Clients connect to `https://<name>.bb.internal:8443` (your app receives it as `<NAME>_URL`) and Envoy presents its own certificate from the lab CA. With one upstream every request goes there; with several, `/<route>/` reaches each one with the prefix rewritten to `/`, and the first upstream also takes everything else. Each upstream hop has its own TLS mode:

- **Re-encrypt** (`tls: {mode: on}`, the default): Envoy decrypts, then opens a new TLS connection to the backend with SNI set to the backend's name, verifies its certificate against the lab CA and checks that the name is on it.
- **Terminate** (`tls: {mode: off, reason: ...}`): Envoy decrypts and sends plaintext HTTP to the backend over a private network shared only with it. An app behind a terminating hop binds `BB_PLAIN_LISTEN_ADDR`, so nothing else can reach its plaintext port.
- **Passthrough** (`tls: {mode: passthrough}`): Envoy never decrypts. On port 9443 (`<NAME>_PASSTHROUGH_ADDR`) the TLS inspector reads the server name (SNI) from the client's hello and picks a filter chain that forwards the bytes, so the client sees the backend's own certificate.

The generated bootstrap is at `.bb/runtime/config/<name>/envoy.yaml`, commented for copying. Every app replica is found through Docker's DNS (`STRICT_DNS`), balanced round robin, and health checked every 2 seconds.

## Key concepts

- **Listeners, filter chains and filters.** A listener accepts connections on a port. A filter chain is picked per connection (here by SNI on 9443), and its network filters handle the bytes: the HTTP connection manager parses HTTP and routes it, the TCP proxy just forwards.
- **Routes and clusters.** The HTTP connection manager matches each request against routes in order; the first match names a cluster. A cluster is a pool of upstream hosts with a load-balancing policy, a transport socket (TLS or not) and health checks.
- **`STRICT_DNS`.** Every address the cluster's DNS name resolves to becomes a host. Envoy re-resolves every 5 seconds, so replicas that start or stop are picked up without a restart.
- **Active health checks.** Envoy sends `GET /` to every host every 2 seconds; three failures take a host out of rotation and two successes bring it back. Any answer below 500 counts as healthy, so apps without a `/` handler stay in. Health checks use the cluster's transport socket, so on a re-encrypt hop they prove the TLS handshake and the certificate too.
- **Panic threshold.** When fewer than half of a cluster's hosts are healthy, Envoy balances across all of them anyway, on the theory that a degraded service beats none.
- **`use_remote_address`.** Envoy is the edge: it sets `X-Forwarded-Proto` from the connection and appends the client's address to `X-Forwarded-For`, so the backend still learns both after termination.
- **The admin API.** It listens on `127.0.0.1:15000` only. `/ready` drives the container's health check; `/clusters` and `/stats` show what Envoy thinks of every host.

## Try this

- Read the cluster state from inside the container: `docker exec bb-<experiment>-<name>-1 bash -c "exec 3<>/dev/tcp/127.0.0.1/15000; printf 'GET /clusters HTTP/1.0\r\n\r\n' >&3; cat <&3"` and find `health_flags` and `rq_success` for each host.
- Give an app `replicas: 3` and call `$<NAME>_URL` thirty times: the answering instance rotates.
- Add a second upstream and reach it at `/<route>/`; the first upstream keeps every other path.
- In your own copy (`file:`), switch `lb_policy` to `LEAST_REQUEST` or `RANDOM`, or point `http_health_check.path` at your app's health endpoint and narrow `expected_statuses` to `200`.
- Connect to `$<NAME>_PASSTHROUGH_ADDR` with `openssl s_client -servername <backend>.bb.internal` and read whose certificate you get.

## Break this

- Stop one replica (`docker stop bb-<experiment>-<app>-2-1`): `/clusters` marks it `/failed_active_hc` within about 6 seconds while requests keep succeeding on the others.
- Make your app answer `/` with a 500: Envoy takes every replica out of rotation, then, with all of them unhealthy, panics and sends traffic to all of them anyway.
- Point a re-encrypt upstream at an app whose certificate does not carry the expected name: the handshake fails the SAN match and requests get 503.
- Try to reach a terminated app's plaintext port from any container but Envoy: it is not on that network.
- Send passthrough traffic with an SNI no upstream claims: no filter chain matches and Envoy closes the connection.
