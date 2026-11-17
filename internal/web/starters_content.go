package web

// starterContent is the narrative a starter page adds to what the starter
// itself provides (files, apps, README exercises): why it exists, how it
// works step by step, and which file holds the code worth reading first.
type starterContent struct {
	Area     string // the technology the breadcrumb and docs link point to
	About    string
	Steps    []step
	KeyFile  string // path inside the starter
	KeyTitle string
	Covers   []string
	Guide    string // docs path of the guide that explains the concept
}

type step struct{ Title, Text string }

var starterContents = map[string]starterContent{
	"kafka/producer-consumer": {
		Area:  "kafka",
		About: "The smallest working Kafka pipeline: one app writes keyed records to a topic, another reads them as a member of a consumer group. Everything else in Kafka builds on these two loops.",
		Steps: []step{
			{"The producer creates the orders topic", "With three partitions, through the admin client. The broker never creates topics on its own."},
			{"Each record is keyed by customer", "The key's hash picks the partition, so one customer's records stay in order on one partition."},
			{"The consumer joins orders-consumers", "As the only member it owns all three partitions. Start a second consumer and they split."},
			{"Offsets are committed every few seconds", "A crash before a commit replays those records: at-least-once delivery."},
		},
		KeyFile: "cmd/consumer/main.go", KeyTitle: "The consumer loop",
		Covers: []string{"Topics and partitions", "Keys and ordering", "Consumer groups", "Offsets and at-least-once delivery"},
		Guide:  "/docs/kafka/consumer-groups",
	},
	"kafka/sink-to-postgres": {
		Area:  "kafka",
		About: "A producer publishes order events to Kafka; a separate sink reads them and keeps a Postgres table in sync. It is the shape of most event-driven pipelines: the producer never talks to the database, and the sink can be stopped, replayed or scaled without the producer noticing.",
		Steps: []step{
			{"The producer keys each event by order ID", "Same key, same partition: every event for one order stays in order."},
			{"The sink joins the group orders-sink", "Kafka assigns it all three partitions. Start a second sink and they split."},
			{"Each record becomes an idempotent insert", "INSERT ... ON CONFLICT (id) DO NOTHING means applying the same event twice leaves one row."},
			{"Offsets are committed after the write", "Crash before the commit and the batch is redelivered. That is at-least-once delivery, and step 3 is why it is safe."},
		},
		KeyFile: "cmd/sink/main.go", KeyTitle: "The sink loop",
		Covers: []string{"Consumer groups", "At-least-once delivery", "Idempotent inserts", "Schema migrations"},
		Guide:  "/docs/kafka/offsets-and-delivery",
	},
	"redis/cache-invalidation": {
		Area:  "redis",
		About: "Prices live in Postgres; a reader serves them through a Redis cache; every price change is announced on Kafka so the stale cache entry is deleted. It shows why caches go stale, and why the order of the write and the announcement matters.",
		Steps: []step{
			{"The writer updates Postgres first", "The database is the source of truth, so the change is committed before anyone hears of it."},
			{"Then it announces the change on Kafka", "One record per changed product on product-updates."},
			{"The invalidator deletes the cache key", "The next read misses and loads the fresh price from Postgres."},
			{"The reader is cache-aside with a TTL", "Redis first, Postgres on a miss, then cache the price for 30 seconds. The TTL bounds how long a missed invalidation can serve a stale price."},
		},
		KeyFile: "cmd/cache-reader/main.go", KeyTitle: "The cache-aside read",
		Covers: []string{"Cache-aside reads", "Invalidate on write", "TTLs as a safety net", "Stale-read races"},
		Guide:  "/docs/redis",
	},
	"s3/objects-basics": {
		Area:  "s3-compatible",
		About: "One app walks through the object storage basics against Breadboard's S3 Compatible service with the official AWS SDK: buckets, keys and prefixes, a multipart upload, and a presigned URL that a client without credentials can use.",
		Steps: []step{
			{"Create a bucket", "Buckets hold objects; their names are global to the server."},
			{"Write keys under notes/", "Keys are flat names. Listing with a prefix and a / delimiter makes them look like folders."},
			{"Upload 11 MiB in parts", "Parts of at least 5 MiB upload independently; completing the upload stitches them together."},
			{"Presign a download", "The signed URL carries an expiry and a signature, so plain HTTP can fetch that one object."},
		},
		KeyFile: "cmd/objects/main.go", KeyTitle: "The walkthrough",
		Covers: []string{"Buckets and keys", "Prefix listing", "Multipart uploads", "Presigned URLs"},
		Guide:  "/docs/s3-compatible",
	},
	"consul/service-discovery": {
		Area:  "consul",
		About: "The recommended way to find services by name. A greeter serves HTTPS and contains no Consul code; Breadboard runs a Consul agent next to it that registers it with a health check. A caller asks Consul for healthy greeters and calls one.",
		Steps: []step{
			{"register: {port: 8443} on the greeter", "Breadboard adds greeter-agent, a Consul client agent sharing the greeter's address and certificate."},
			{"The agent registers the greeter", "With a check that opens a TLS connection to it every 5 seconds."},
			{"The caller looks up healthy greeters", "Health().Service returns only instances whose checks pass."},
			{"Stop the greeter", "Its check fails, Consul stops returning it, and the caller notices without any code change."},
		},
		KeyFile: "cmd/caller/main.go", KeyTitle: "Finding a service by name",
		Covers: []string{"Sidecar registration", "Health-checked catalog", "Lookup by name", "Failure removal"},
		Guide:  "/docs/consul",
	},
	"consul/self-registration": {
		Area:  "consul",
		About: "The alternative to consul/service-discovery: here the service registers and deregisters itself through the Consul API. Read it to see the plumbing the recommended sidecar removes.",
		Steps: []step{
			{"The greeter registers itself on start", "With its own address from BB_FQDN and a TLS check."},
			{"The caller looks up healthy greeters", "The same lookup as with the sidecar."},
			{"On SIGTERM it deregisters", "Cleanup is now the service's job, so it must receive the signal."},
			{"A crash skips cleanup", "DeregisterCriticalServiceAfter removes the entry after its check has failed for a minute."},
		},
		KeyFile: "cmd/self-greeter/main.go", KeyTitle: "Registering itself",
		Covers: []string{"Agent API registration", "Deregistration on shutdown", "Crash safety nets", "Why sidecars exist"},
		Guide:  "/docs/consul",
	},
	"sidecar/envoy-ambassador": {
		Area:  "sidecars",
		About: "A caller talks plain HTTP to 127.0.0.1 and its Envoy sidecar does the rest: picks one of three backend replicas, speaks verified TLS to it, applies a timeout and retries a failed attempt elsewhere. The backend has no TLS code either; its own sidecar terminates TLS. The caller's log tallies which replica answered.",
		Steps: []step{
			{"The caller calls AMBASSADOR_BACKEND_URL", "http://127.0.0.1:9001 is a listener of its Envoy sidecar, the ambassador. The code knows nothing else."},
			{"The ambassador resolves ambassador-backend", "The shared name resolves to all three replicas; Envoy keeps each address as a host and re-resolves every 5 seconds."},
			{"Round robin over verified TLS", "Each request goes to the next replica, over TLS checked against the lab CA for that name. timeout: 2s and retries: 2 apply per call."},
			{"Each replica's sidecar terminates TLS", "Envoy accepts TLS on 8443 with the replica's certificate and forwards to the app on 127.0.0.1:8080."},
		},
		KeyFile: "cmd/ambassador-caller/main.go", KeyTitle: "A caller with no balancing code",
		Covers: []string{"The ambassador pattern", "Client-side load balancing", "Retries and timeouts in config", "Replicas behind one name"},
		Guide:  "/docs/sidecars/load-balancing",
	},
	"sidecar/envoy-tls-termination": {
		Area:  "sidecars",
		About: "A plain HTTP app with zero TLS code serves HTTPS anyway: an Envoy sidecar in its pod terminates TLS with the app's certificate and forwards to it on loopback. A client verifies the certificate, and also shows that the app's own port cannot be reached from another container.",
		Steps: []step{
			{"plain-web binds BB_LISTEN_ADDR", "127.0.0.1:8080, inside its pod. A loopback socket is invisible to other containers."},
			{"Envoy listens on 8443", "With plain-web's certificate from the lab CA, accepting only TLS."},
			{"It forwards over loopback", "The sidecar shares the app's network namespace, so 127.0.0.1:8080 is the app. X-Forwarded-Proto tells the app the client used https."},
			{"tls-client verifies and probes", "It calls https://plain-web.<domain>:8443 with Go's default verification, then tries :8080 directly and is refused."},
		},
		KeyFile: "cmd/plain-web/main.go", KeyTitle: "A server with no TLS code",
		Covers: []string{"TLS termination at a sidecar", "Loopback-only binding", "Certificate verification", "No way around the sidecar"},
		Guide:  "/docs/sidecars/patterns",
	},
	"sidecar/envoy-transparent": {
		Area:  "sidecars",
		About: "The caller uses real names in plain HTTP and has no idea a proxy exists. iptables rules installed by an init container send every connection to its Envoy sidecar, which upgrades calls to declared services to verified TLS and refuses the rest: an egress policy, the root of service-to-service access control.",
		Steps: []step{
			{"netinit installs the rules", "A one-shot container with NET_ADMIN redirects all outgoing TCP to Envoy on 15001, except Envoy's own (uid 101) and loopback."},
			{"Envoy recovers the destination", "The original_dst listener filter reads where the app meant to connect before the redirect."},
			{"Declared: TLS on the wire", "A plain HTTP request for transparent-backend on 8443 leaves as TLS, verified against the lab CA."},
			{"Undeclared: refused in the pod", "With egress: deny, the request for transparent-stranger gets 403 from Envoy itself and never leaves."},
		},
		KeyFile: "cmd/transparent-caller/main.go", KeyTitle: "Calls by name, no proxy settings",
		Covers: []string{"Transparent proxying", "iptables redirect and original destination", "Egress allow and deny", "Ambassador vs transparent"},
		Guide:  "/docs/sidecars/transparent-proxy",
	},
	"sidecar/circuit-breaking": {
		Area:  "sidecars",
		About: "Three backend replicas, one made to fail on demand. The caller's Envoy sidecar counts consecutive 5xx per host and ejects a failing one for a while, then lets it back in. The starter raises max_ejection_percent to 50: Envoy's default of 10% would refuse to eject one host of three, which is 33% of the cluster.",
		Steps: []step{
			{"POST /fail on replica 3", "It answers 503 to everything. retries: 0 on the caller's outbound keeps every failure visible."},
			{"Three consecutive 5xx, ejected", "Envoy ejects the host at once; with max_ejection_percent: 50 one host of three may go. The caller's tally shows only replicas 1 and 2."},
			{"Back after the ejection time", "After 30 s, checked on the 5 s sweep, replica 3 gets traffic again; it stays if it has recovered, or is ejected for twice as long."},
			{"Put the default back", "With max_ejection_percent: 10 nothing is ejected: the Envoy explorer counts each ejection held back by the limit."},
		},
		KeyFile: "cmd/breaker-caller/main.go", KeyTitle: "A tally with no resilience code",
		Covers: []string{"Outlier detection", "max_ejection_percent", "Ejection time and backoff", "Retries vs ejection"},
		Guide:  "/docs/sidecars/load-balancing",
	},
	"sidecar/pgbouncer-pool": {
		Area:  "sidecars",
		About: "Fifty workers, each with its own database connection, while Postgres sees at most ten. A PgBouncer sidecar in transaction mode lends server connections one transaction at a time; the app's DATABASE_URL points at it on loopback, and it talks to Postgres over verified TLS.",
		Steps: []step{
			{"DATABASE_URL is rewritten", "It points at PgBouncer on 127.0.0.1:6432 with sslmode=disable: that hop never leaves the pod."},
			{"pgxpool opens 50 connections", "All of them to PgBouncer, which accepts up to 200 clients."},
			{"Transaction mode lends servers", "A client holds one of 10 server connections only while its transaction runs; the rest wait in PgBouncer's queue."},
			{"Postgres counts 10", "The app asks pg_stat_activity every 5 seconds and logs the server connections next to its 50 clients."},
		},
		KeyFile: "cmd/pool-client/main.go", KeyTitle: "50 clients, one pool",
		Covers: []string{"Connection pooling", "Transaction vs session mode", "pool_size vs client count", "Pooler-safe drivers"},
		Guide:  "/docs/sidecars/connection-pooling",
	},
	"sidecar/log-shipper": {
		Area:  "sidecars",
		About: "The app writes JSON log lines to a file and nothing else. A Fluent Bit sidecar tails the shared log directory and uploads the lines to a bucket on S3 Compatible storage, where a second app finds them with the AWS SDK. Changing where logs go never touches the app: the adapter pattern.",
		Steps: []step{
			{"log-writer writes to BB_LOG_DIR", "One slog JSON line a second to /var/run/bb/logs/app.log, a directory shared by the pod."},
			{"Fluent Bit tails the file", "It remembers its offset in a small database in the shared directory, so a restart does not ship lines twice."},
			{"Uploads every 10 seconds", "Newline-delimited JSON objects under log-writer/<date>/, over TLS verified against the lab CA."},
			{"log-reader lists the bucket", "With the official AWS SDK, and prints the newest object's first record: the original line, wrapped."},
		},
		KeyFile: "cmd/log-writer/main.go", KeyTitle: "An app that only writes files",
		Covers: []string{"The adapter pattern", "Logs as files in a shared volume", "Fluent Bit tail and S3 output", "Finding objects with the AWS SDK"},
		Guide:  "/docs/sidecars/patterns",
	},
	"lb/round-robin": {
		Area:  "nginx",
		About: "Three replicas behind NGINX, which sends each request to the next one in turn. NGINX finds the replicas through Docker's DNS and looks again every 5 seconds, so scaling the backend changes the rotation without touching NGINX. The config is a commented nginx.conf the starter writes for you to own.",
		Steps: []step{
			{"One name, every replica", "The upstream names rr-backend once, with resolve: each address the name resolves to becomes a peer."},
			{"Next peer, every request", "No method is named, so NGINX uses weighted round robin with every weight at 1. One worker process keeps a single rotation."},
			{"Re-encrypt to the replica", "NGINX opens its own TLS connection, verified against the lab CA, and keeps it alive: balancing is per request, not per connection."},
			{"Scale and it follows", "Set replicas: 4 and run bb up: within seconds each replica answers a quarter, and NGINX was never restarted."},
		},
		KeyFile: "nginx.conf", KeyTitle: "The upstream block",
		Covers: []string{"Round robin", "DNS re-resolution", "Scaling replicas", "Passive health checks"},
	},
	"lb/least-connections": {
		Area:  "haproxy",
		About: "HAProxy balances the same three replicas two ways at once: leastconn on one path, roundrobin on another. One replica takes ten times longer per request. Round robin keeps giving it a third of the traffic; least connections notices that its requests pile up and sends it a few percent.",
		Steps: []step{
			{"Uneven cost", "Requests take 50 ms on two replicas and 500 ms on the third. The client keeps six requests in flight per strategy."},
			{"roundrobin ignores it", "Each request goes to the next server in turn, so a third of them wait 500 ms."},
			{"leastconn counts", "Each request goes to the server with the fewest requests in progress; the slow one keeps a high count and loses."},
			{"Read HAProxy's own counts", "The HAProxy stats tab shows how often each server was picked in each backend."},
		},
		KeyFile: "haproxy.cfg", KeyTitle: "Two backends, two algorithms",
		Covers: []string{"Least connections", "Round robin under uneven cost", "Tail latency", "server-template and DNS"},
	},
	"lb/power-of-two-choices": {
		Area:  "traefik",
		About: "Traefik picks two replicas at random for each request and sends it to the less busy one. Next to weighted round robin over the same replicas, the slow replica gets far fewer requests, yet every replica keeps serving. Two random choices give most of the benefit of least connections with no global view.",
		Steps: []step{
			{"Same servers, two services", "Services p2c and wrr list the three replicas by name; routers send /wrr/ to one and the rest to the other."},
			{"Two random picks", "p2c compares the requests in flight on two random servers and takes the lower."},
			{"The slow one loses", "Replica 3 holds its requests ten times longer, so it wins only when it happens to be idle: a few percent of requests."},
			{"Other strategies", "Switch to leasttime (learns response times) or hrw (sticks a client to one server) in the routing file."},
		},
		KeyFile: "dynamic.yml", KeyTitle: "Services p2c and wrr",
		Covers: []string{"Power of two choices", "Weighted round robin", "Traefik load-balancing strategies", "Health checks"},
	},
	"lb/consistent-hashing": {
		Area:  "envoy",
		About: "Envoy hashes the X-User-ID header to choose one of four replicas, with ring_hash and maglev side by side. Each replica caches the keys it sees, so the same key landing on the same replica keeps every cache warm. Stop a replica and the client counts how many of its 200 keys move: all of the stopped replica's, and with ring_hash a few others.",
		Steps: []step{
			{"Hash the header", "Each route's hash_policy hashes x-user-id; without the header Envoy would pick a host at random."},
			{"Ring or table", "ring_hash places each host on a ring many times; maglev fills a 65537-entry table. Both map a key to one host."},
			{"Same key, same replica", "Round after round no key moves, and every request is a cache hit."},
			{"Remove a replica", "docker stop one of four: its keys move to the others, and most keys stay put with their cache."},
		},
		KeyFile: "envoy.yaml", KeyTitle: "Two hashing clusters",
		Covers: []string{"Consistent hashing", "Ring hash vs Maglev", "Cache locality", "Remapping when a replica leaves"},
	},
}
