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
}
