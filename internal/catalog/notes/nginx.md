# NGINX

## What you got

NGINX 1.30 (open source) in front of the upstreams you list under `services.<name>.upstreams`. Clients connect to `https://<name>.bb.internal:8443` (your app receives it as `<NAME>_URL`) and NGINX presents its own certificate from the lab CA. With one upstream every request goes there; with several, `/<route>/` reaches each one with the prefix stripped, and the first upstream also takes everything else. Each upstream hop has its own TLS mode:

- **Re-encrypt** (`tls: {mode: on}`, the default): NGINX decrypts, then opens a new TLS connection to the backend (`proxy_ssl_verify on`), checks its certificate against the lab CA and sends the backend's name as SNI (`proxy_ssl_server_name`, `proxy_ssl_name`).
- **Terminate** (`tls: {mode: off, reason: ...}`): NGINX decrypts and sends plain HTTP to the backend over a private network shared only with it. An app behind a terminating hop binds `BB_PLAIN_LISTEN_ADDR`, so nothing else can reach its plaintext port.
- **Passthrough** (`tls: {mode: passthrough}`): the `stream` module never decrypts. `ssl_preread` reads the server name (SNI) from the client's TLS hello on port 9443 (`<NAME>_PASSTHROUGH_ADDR`) and a `map` picks the upstream, so the client sees the backend's own certificate.

The generated config is at `.bb/runtime/config/<name>/nginx.conf`, commented for copying. Every app replica is found through Docker's DNS: each `upstream` has a `zone` and one `server <app>.bb.internal:<port> resolve`, re-resolved every 5 seconds, balanced round robin.

## Key concepts

- **`http` versus `stream`.** The `http` block parses requests, so it can route on paths and set headers. The `stream` block only moves bytes, which is why passthrough routes on SNI and cannot see paths.
- **`upstream` with `resolve`.** Open source NGINX re-resolves a server name at runtime since 1.27.3, as long as the upstream has a shared-memory `zone` and a `resolver` is set. Scale the app and NGINX follows.
- **Passive health checks.** NGINX notices failures on real requests: after `max_fails=3` failures within `fail_timeout=10s`, an address rests for 10 seconds and the request is retried on the next one. A failure is what `proxy_next_upstream` counts, `error timeout` by default: a connection, send or response-header error or timeout, or an invalid response. An HTTP 5xx answer is not a failure, so a replica that answers 500 to everything keeps its share. In your own copy of the config, `proxy_next_upstream error timeout http_500 http_502 http_503 http_504;` counts those statuses too, and also retries them on the next server: only safe when those requests can run twice (NGINX never retries a POST, PATCH or LOCK once sent, unless you add `non_idempotent`); cap retries with `proxy_next_upstream_tries`. Active `health_check` probes are NGINX Plus only, so a dead replica is found by traffic, not by a probe.
- **`proxy_pass` with or without a URI.** `proxy_pass https://be_api/;` (with the trailing `/`) replaces the matched prefix `/api/` with `/`; `proxy_pass https://be_web;` passes the path untouched.
- **Keepalive to upstreams.** `keepalive 16` keeps idle connections open per worker, so most requests skip the TCP and TLS handshakes to the backend.
- **`X-Forwarded-Proto` and `X-Forwarded-For`.** After termination the backend no longer sees TLS or the client's address; these headers tell it.

## Try this

- Read the counters from inside the container: `docker exec bb-<experiment>-<name>-1 wget -qO- http://127.0.0.1:8404/stub_status` (active connections, accepts, handled, requests).
- Give an app `replicas: 3` and call `$<NAME>_URL` thirty times: the answering instance rotates.
- Add a second upstream and reach it at `/<route>/`; the first upstream keeps every other path.
- Copy the config (`file:`) and try another open source balancing method in an `upstream` block:
  - `least_conn;` sends each request to the address with the fewest active connections;
  - `ip_hash;` keeps a client on one address by its IP (HTTP only);
  - `hash $request_uri consistent;` keeps a key on one address, and moves few keys when addresses come and go;
  - `random two least_conn;` picks two addresses at random and uses the less busy one;
  - `sticky cookie srv_id expires=1h;` pins a client to an address with a cookie (open source since 1.29.6).
- Connect to `$<NAME>_PASSTHROUGH_ADDR` with SNI `<backend>.bb.internal` and read whose certificate you get.

## Break this

- Stop one replica (`docker stop bb-<experiment>-<app>-2-1`): the next requests that hit it fail over to another replica, and NGINX logs `upstream ... failed` until DNS drops the address.
- Point a re-encrypt upstream at an app that serves a certificate for another name: NGINX logs `upstream SSL certificate does not match` and answers 502.
- Try to reach a terminated app's plaintext port from any container but NGINX: it is not on that network.
- Send passthrough traffic with an SNI no upstream claims: it maps to `bb_unknown_sni`, which has no live server, and NGINX closes the connection.
