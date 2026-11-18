# Traefik

## What you got

Traefik 3.7 in front of the upstreams you list under `services.<name>.upstreams`, configured by files only: the file provider reads `/etc/bb/traefik`, and Traefik never sees the Docker socket. Clients connect to `https://<name>.bb.internal:8443` (your app receives it as `<NAME>_URL`) and Traefik presents its own certificate from the lab CA. With one upstream every request goes there; with several, `/<route>/` reaches each one with the prefix stripped, and the first upstream also takes everything else. Each upstream hop has its own TLS mode:

- **Re-encrypt** (`tls: {mode: on}`, the default): Traefik decrypts, then opens a new TLS connection to the backend through a `serversTransport` that sets SNI to the backend's name and verifies its certificate against the lab CA.
- **Terminate** (`tls: {mode: off, reason: ...}`): Traefik decrypts and sends plaintext HTTP to the backend over a private network shared only with it. An app behind a terminating hop binds `BB_PLAIN_LISTEN_ADDR`, so nothing else can reach its plaintext port.
- **Passthrough** (`tls: {mode: passthrough}`): Traefik never decrypts. On port 9443 (`<NAME>_PASSTHROUGH_ADDR`) a TCP router matches the server name (`HostSNI`) and forwards the bytes, so the client sees the backend's own certificate.

The configuration is in `.bb/runtime/config/<name>/`, commented for copying:

- `traefik.yml` is the static (install) configuration: entrypoints, the file provider, `ping` and the API. Breadboard owns it. `static_file:` adds your own sections to it (metrics, tracing, plugins, a global `serversTransport`); `log` and `accessLog` from your file replace Breadboard's, and `entryPoints`, `providers`, `api`, `ping` and `global` are refused.
- `dynamic.yml` is the routing: routers, middlewares, services and transports. `file:` replaces this one.
- `tls.yml` is the certificate store and the minimum TLS version. It stays when you own `dynamic.yml`.

## Key concepts

- **Static versus dynamic configuration.** Static configuration is read once at startup (where to listen, where to find routes). Dynamic configuration comes from providers and can change at runtime; the file provider watches its directory and applies edits without a restart.
- **Entrypoints, routers, services.** An entrypoint is a listening port (`websecure` on 8443, `passthrough` on 9443). A router matches requests on an entrypoint with a rule (`PathPrefix`, `Host`, `HostSNI`) and hands them to a service, optionally through middlewares. A service balances across servers.
- **Priority.** By default a router's priority is the length of its rule, so ``PathPrefix(`/api/`)`` beats ``PathPrefix(`/`)`` without any setting.
- **Middlewares.** `stripPrefix` removes `/<route>` before forwarding and puts it in `X-Forwarded-Prefix`. Others add headers, rate limits, retries or authentication.
- **Servers are listed one by one.** Traefik does not turn one DNS name into several servers, so the generated service lists every replica (`<app>-1`, `<app>-2`, ...). Add a replica and its server appears when Breadboard regenerates the file.
- **Load-balancing strategies.** `loadBalancer.strategy` is `wrr` (weighted round robin, the default). Traefik 3.7 also has `p2c` (power of two random choices, picking the one with fewer active requests), `hrw` (highest random weight: the same client sticks to the same server) and `leasttime` (favours the servers that answer fastest). Switch in your own copy with `file:`.
- **Health checks.** Every app server gets `GET /` every 2 seconds; a 2xx or 3xx answer keeps it in. A failing server leaves the rotation until it passes again. Infra upstreams have one server and no check: a check that failed would only turn a working route into a 503.
- **Encoded characters in the path.** Every entrypoint sets `http.encodedCharacters` with all seven options `false`, so a path holding an encoded `/`, `\`, NUL, `;`, `%`, `?` or `#` (`%2F`, `%5C`, `%00`, `%3B`, `%25`, `%3F`, `%23`) gets a 400 before any router sees it. A backend that decodes the path again could otherwise act on a different path than the one Traefik routed (a split view). The query string is not checked.
- **The API and dashboard.** They listen on the `traefik` entrypoint at `127.0.0.1:8080` only, next to `/ping`, which the container's health check (`traefik healthcheck --ping`) reads.

## Try this

- List what Traefik loaded, with each server's status: `docker exec bb-<experiment>-<name>-1 wget -qO- http://127.0.0.1:8080/api/http/services`.
- Give an app `replicas: 3` and call `$<NAME>_URL` thirty times: the answering instance rotates.
- Copy `dynamic.yml`, set `file:`, and change `strategy` to `p2c` or `hrw`; or give one server `weight: 3` and count how often it answers.
- Turn on Prometheus metrics: write `metrics: {prometheus: {entryPoint: traefik}}` to `traefik-static.yml` next to `experiment.yaml`, set `static_file: ./traefik-static.yml`, run `bb up`, and read `docker exec bb-<experiment>-<name>-1 wget -qO- http://127.0.0.1:8080/metrics | grep traefik_entrypoint_requests_total`.
- Add a `headers` middleware that sets a response header, attach it to a router, and find the header in your app's responses through the proxy.
- Connect to `$<NAME>_PASSTHROUGH_ADDR` with `openssl s_client -servername <backend>.bb.internal` and read whose certificate you get.

## Break this

- Stop one replica (`docker stop bb-<experiment>-<app>-2-1`): the API shows its server `DOWN` within seconds while requests keep succeeding on the others.
- Make your app answer `/` with a 404: the health check fails every server and Traefik answers 503. Point `healthCheck.path` at an endpoint that returns 200.
- Set a `serversTransport`'s `serverName` to another app's name in your copy: the backend's certificate no longer matches, so the handshake fails and requests get an error instead of an answer.
- Try to reach a terminated app's plaintext port from any container but Traefik: it is not on that network.
- Request `$<NAME>_URL/api%2F..%2Fadmin`: Traefik answers 400 and your app never sees the request. The policy is in `traefik.yml`, which stays Breadboard's.
- Send passthrough traffic with an SNI no router claims: Traefik closes the connection.
