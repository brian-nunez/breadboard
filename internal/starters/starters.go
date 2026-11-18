// Package starters lists starters and renders their templates. Applying a
// starter to a project (writing files, editing experiment.yaml) is a core
// operation; this package only knows what each starter contains.
package starters

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"text/template"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
	templates "github.com/brian-nunez/breadboard/starters"
)

// Toolchain versions written into every Go starter.
const (
	GoVersion     = "1.27.4"
	GoImage       = "1.27.4-alpine3.24"
	AlpineVersion = "3.24.4"
)

// Starter is one starter's definition.
type Starter struct {
	ID       string
	Title    string
	Summary  string
	Language string
	// Template is the directory in the embedded templates.
	Template string
	// Dir is the default directory the code is written to, relative to the project.
	Dir string
	// Requires lists catalog types the starter needs; missing ones are added.
	Requires []string
	Apps     []App
	// Services are proxies the starter adds in front of its apps.
	Services []Service
}

// App is an app service the starter adds.
type App struct {
	Name string
	// Command runs the app: the binary the Dockerfile's dev stage built
	// from cmd/<name> into /usr/local/bin.
	Command []string
	// Uses lists catalog types, each resolved to the experiment's service of
	// that type, or the names of this starter's Services.
	Uses []string
	// Register, when set, registers the app in the registry it uses through a sidecar.
	Register *spec.Register
	// Replicas is the number of instances; zero means one.
	Replicas int
	// Sidecars run in the app's pod. A reference to an infra service
	// (pgbouncer service, fluent-bit to, an outbound service that is not an
	// app of this starter) names its catalog type, which resolves like Uses.
	Sidecars []spec.Sidecar
}

// Service is a proxy service the starter adds: an infra service of a proxy
// type, with upstreams and, usually, a native config the learner owns.
type Service struct {
	// Name is unique across every starter, like app names.
	Name string
	// Type is a proxy type from the catalog (haproxy, envoy, traefik,
	// nginx, pgbouncer).
	Type string
	// Upstreams are the proxy's backends. Each Service names an app of
	// this starter or a catalog type in Requires, which resolves to the
	// experiment's service of that type, like Uses. Port, Name and TLS
	// (mode and reason) are written as given.
	Upstreams []spec.Upstream
	// File is the path of a rendered template file (without .tmpl),
	// relative to the starter's directory. bb starter writes it with the
	// code and sets the service's file to it, relative to experiment.yaml.
	// Empty keeps the generated config.
	File string
	// StaticFile is like File for the service's static_file: a rendered
	// template file merged into the proxy's generated static
	// configuration (Traefik only).
	StaticFile string
	// Config is the service's config, written as given and validated
	// against the catalog entry's options like any config.
	Config map[string]string
}

var all = []Starter{
	{
		ID:       "kafka/producer-consumer",
		Title:    "Kafka producer and consumer",
		Summary:  "Topics, partitions, keys, consumer groups, offsets, rebalancing.",
		Language: "go",
		Template: "go/kafka/producer-consumer",
		Dir:      "producer-consumer",
		Requires: []string{"kafka"},
		Apps: []App{
			{Name: "producer", Command: []string{"producer"}, Uses: []string{"kafka"}},
			{Name: "consumer", Command: []string{"consumer"}, Uses: []string{"kafka"}},
		},
	},
	{
		ID:       "kafka/sink-to-postgres",
		Title:    "Kafka sink to Postgres",
		Summary:  "Consume, transform, write; idempotent inserts; at-least-once delivery; migrations.",
		Language: "go",
		Template: "go/kafka/sink-to-postgres",
		Dir:      "sink-to-postgres",
		Requires: []string{"kafka", "postgres"},
		Apps: []App{
			{Name: "sink-producer", Command: []string{"sink-producer"}, Uses: []string{"kafka"}},
			{Name: "sink", Command: []string{"sink"}, Uses: []string{"kafka", "postgres"}},
		},
	},
	{
		ID:       "redis/cache-invalidation",
		Title:    "Redis cache invalidation",
		Summary:  "Cache-aside, invalidate-on-write via Kafka events, TTLs, stale reads.",
		Language: "go",
		Template: "go/redis/cache-invalidation",
		Dir:      "cache-invalidation",
		Requires: []string{"kafka", "postgres", "redis"},
		Apps: []App{
			{Name: "cache-writer", Command: []string{"cache-writer"}, Uses: []string{"postgres", "kafka"}},
			{Name: "cache-reader", Command: []string{"cache-reader"}, Uses: []string{"postgres", "redis"}},
			{Name: "cache-invalidator", Command: []string{"cache-invalidator"}, Uses: []string{"kafka", "redis"}},
		},
	},
	{
		ID:       "s3/objects-basics",
		Title:    "S3 objects basics",
		Summary:  "Buckets, keys and prefixes, multipart upload, presigned URLs against S3 Compatible storage.",
		Language: "go",
		Template: "go/s3/objects-basics",
		Dir:      "objects-basics",
		Requires: []string{"s3-compatible"},
		Apps: []App{
			{Name: "objects", Command: []string{"objects"}, Uses: []string{"s3-compatible"}},
		},
	},
	{
		ID:       "consul/service-discovery",
		Title:    "Consul service discovery (recommended)",
		Summary:  "Recommended: a Consul sidecar agent registers each service; a health-checked catalog; resolving services by name.",
		Language: "go",
		Template: "go/consul/service-discovery",
		Dir:      "service-discovery",
		Requires: []string{"consul"},
		Apps: []App{
			{Name: "greeter", Command: []string{"greeter"}, Uses: []string{"consul"}, Register: &spec.Register{Port: 8443}},
			{Name: "caller", Command: []string{"caller"}, Uses: []string{"consul"}},
		},
	},
	{
		ID:       "consul/self-registration",
		Title:    "Consul self-registration (alternative)",
		Summary:  "Alternative to consul/service-discovery: each service registers and deregisters itself through the Consul API.",
		Language: "go",
		Template: "go/consul/self-registration",
		Dir:      "self-registration",
		Requires: []string{"consul"},
		Apps: []App{
			{Name: "self-greeter", Command: []string{"self-greeter"}, Uses: []string{"consul"}},
			{Name: "self-caller", Command: []string{"self-caller"}, Uses: []string{"consul"}},
		},
	},
	{
		ID:       "sidecar/envoy-ambassador",
		Title:    "Envoy ambassador",
		Summary:  "An Envoy sidecar balances calls across three replicas over TLS, with retries and timeouts in config, not code.",
		Language: "go",
		Template: "go/sidecar/envoy-ambassador",
		Dir:      "envoy-ambassador",
		Apps: []App{
			{Name: "ambassador-backend", Command: []string{"ambassador-backend"}, Replicas: 3, Sidecars: []spec.Sidecar{envoyInbound()}},
			{Name: "ambassador-caller", Command: []string{"ambassador-caller"}, Sidecars: []spec.Sidecar{envoyOutbound(spec.EnvoyOutbound{
				Service: "ambassador-backend", Port: 9001, LB: "round_robin", Timeout: "2s", Retries: intPtr(2),
			})}},
		},
	},
	{
		ID:       "sidecar/envoy-tls-termination",
		Title:    "Envoy TLS termination",
		Summary:  "A plain HTTP app with no TLS code, bound to loopback, served over verified TLS by its Envoy sidecar.",
		Language: "go",
		Template: "go/sidecar/envoy-tls-termination",
		Dir:      "envoy-tls-termination",
		Apps: []App{
			{Name: "plain-web", Command: []string{"plain-web"}, Sidecars: []spec.Sidecar{envoyInbound()}},
			{Name: "tls-client", Command: []string{"tls-client"}},
		},
	},
	{
		ID:       "sidecar/envoy-transparent",
		Title:    "Envoy transparent proxy",
		Summary:  "iptables send every connection to Envoy: plain HTTP calls by name leave as TLS, and egress deny refuses undeclared services.",
		Language: "go",
		Template: "go/sidecar/envoy-transparent",
		Dir:      "envoy-transparent",
		Apps: []App{
			{Name: "transparent-backend", Command: []string{"transparent-backend"}, Sidecars: []spec.Sidecar{envoyInbound()}},
			{Name: "transparent-stranger", Command: []string{"transparent-stranger"}, Sidecars: []spec.Sidecar{envoyInbound()}},
			{Name: "transparent-caller", Command: []string{"transparent-caller"}, Sidecars: []spec.Sidecar{{
				Type: spec.SidecarEnvoy, Envoy: &spec.EnvoySidecar{
					Outbound:    []spec.EnvoyOutbound{{Service: "transparent-backend", Port: 9001}},
					Transparent: &spec.Transparent{Egress: spec.EgressDeny},
				},
			}}},
		},
	},
	{
		ID:       "sidecar/circuit-breaking",
		Title:    "Envoy outlier detection",
		Summary:  "A flaky replica is ejected by the caller's Envoy sidecar after consecutive 5xx, then let back in after the ejection time.",
		Language: "go",
		Template: "go/sidecar/circuit-breaking",
		Dir:      "circuit-breaking",
		Apps: []App{
			{Name: "breaker-backend", Command: []string{"breaker-backend"}, Replicas: 3, Sidecars: []spec.Sidecar{envoyInbound()}},
			{Name: "breaker-caller", Command: []string{"breaker-caller"}, Sidecars: []spec.Sidecar{envoyOutbound(spec.EnvoyOutbound{
				Service: "breaker-backend", Port: 9001, Retries: intPtr(0),
				Outlier: &spec.Outlier{Consecutive5xx: 3, Ejection: "30s", MaxEjectionPercent: 50, Interval: "5s"},
			})}},
		},
	},
	{
		ID:       "sidecar/pgbouncer-pool",
		Title:    "PgBouncer connection pool",
		Summary:  "50 client connections share 10 Postgres connections through a PgBouncer sidecar in transaction mode.",
		Language: "go",
		Template: "go/sidecar/pgbouncer-pool",
		Dir:      "pgbouncer-pool",
		Requires: []string{"postgres"},
		Apps: []App{
			{Name: "pool-client", Command: []string{"pool-client"}, Uses: []string{"postgres"}, Sidecars: []spec.Sidecar{{
				Type: spec.SidecarPgBouncer, PgBouncer: &spec.PgBouncerSidecar{Service: "postgres", PoolMode: "transaction", PoolSize: 10},
			}}},
		},
	},
	{
		ID:       "sidecar/log-shipper",
		Title:    "Fluent Bit log shipper",
		Summary:  "An app writes JSON log files; a Fluent Bit sidecar ships them to S3 Compatible storage, where the AWS SDK finds them.",
		Language: "go",
		Template: "go/sidecar/log-shipper",
		Dir:      "log-shipper",
		Requires: []string{"s3-compatible"},
		Apps: []App{
			{Name: "log-writer", Command: []string{"log-writer"}, Uses: []string{"s3-compatible"}, Sidecars: []spec.Sidecar{{
				Type: spec.SidecarFluentBit, FluentBit: &spec.FluentBitSidecar{To: "s3-compatible", Bucket: "app-logs"},
			}}},
			{Name: "log-reader", Command: []string{"log-reader"}, Uses: []string{"s3-compatible"}},
		},
	},
	{
		ID:       "lb/round-robin",
		Title:    "Round robin with NGINX",
		Summary:  "NGINX sends each request to the next of three replicas it found through DNS; scale the backend and it follows.",
		Language: "go",
		Template: "go/lb/round-robin",
		Dir:      "round-robin",
		Apps: []App{
			{Name: "rr-backend", Command: []string{"rr-backend"}, Replicas: 3},
			{Name: "rr-client", Command: []string{"rr-client"}, Uses: []string{"rr-edge"}},
		},
		Services: []Service{{
			Name: "rr-edge",
			Type: "nginx",
			// rr-backend serves TLS on 8443; NGINX re-encrypts.
			Upstreams: []spec.Upstream{{Service: "rr-backend", Port: 8443}},
			File:      "nginx.conf",
		}},
	},
	{
		ID:       "lb/least-connections",
		Title:    "Least connections with HAProxy",
		Summary:  "HAProxy's leastconn and roundrobin side by side over the same replicas, one of them ten times slower: leastconn sends it far fewer requests.",
		Language: "go",
		Template: "go/lb/least-connections",
		Dir:      "least-connections",
		Apps: []App{
			{Name: "lc-backend", Command: []string{"lc-backend"}, Replicas: 3},
			{Name: "lc-client", Command: []string{"lc-client"}, Uses: []string{"lc-edge"}},
		},
		Services: []Service{{
			Name: "lc-edge",
			Type: "haproxy",
			// Named leastconn, so the explorers tag backend be_leastconn
			// with this upstream; be_roundrobin is the file's own.
			Upstreams: []spec.Upstream{{Service: "lc-backend", Port: 8443, Name: "leastconn"}},
			File:      "haproxy.cfg",
		}},
	},
	{
		ID:       "lb/power-of-two-choices",
		Title:    "Power of two choices with Traefik",
		Summary:  "Traefik's p2c picks the less busy of two random replicas; next to wrr over the same replicas, the slow one gets far fewer requests.",
		Language: "go",
		Template: "go/lb/power-of-two-choices",
		Dir:      "power-of-two-choices",
		Apps: []App{
			{Name: "p2c-backend", Command: []string{"p2c-backend"}, Replicas: 3},
			{Name: "p2c-client", Command: []string{"p2c-client"}, Uses: []string{"p2c-edge"}},
		},
		Services: []Service{{
			Name: "p2c-edge",
			Type: "traefik",
			// Named p2c, so the explorers tag Traefik service p2c with
			// this upstream; service wrr is the file's own.
			Upstreams: []spec.Upstream{{Service: "p2c-backend", Port: 8443, Name: "p2c"}},
			File:      "dynamic.yml",
		}},
	},
	{
		ID:       "lb/consistent-hashing",
		Title:    "Consistent hashing with Envoy",
		Summary:  "Envoy hashes a header to pick a replica, ring_hash next to maglev: the same key always lands on the same replica, and removing one remaps only a minority of keys.",
		Language: "go",
		Template: "go/lb/consistent-hashing",
		Dir:      "consistent-hashing",
		Apps: []App{
			{Name: "ch-backend", Command: []string{"ch-backend"}, Replicas: 4},
			{Name: "ch-client", Command: []string{"ch-client"}, Uses: []string{"ch-edge"}},
		},
		Services: []Service{{
			Name: "ch-edge",
			Type: "envoy",
			// Named ring-hash, so the explorers tag cluster ring-hash with
			// this upstream; cluster maglev is the file's own.
			Upstreams: []spec.Upstream{{Service: "ch-backend", Port: 8443, Name: "ring-hash"}},
			File:      "envoy.yaml",
		}},
	},
	{
		ID:       "lb/sticky-sessions",
		Title:    "Sticky sessions with HAProxy",
		Summary:  "An HAProxy cookie pins each user to one replica that holds their session; stop that replica and the user moves, and the session starts over.",
		Language: "go",
		Template: "go/lb/sticky-sessions",
		Dir:      "sticky-sessions",
		Apps: []App{
			{Name: "sticky-backend", Command: []string{"sticky-backend"}, Replicas: 3},
			{Name: "sticky-client", Command: []string{"sticky-client"}, Uses: []string{"sticky-edge"}},
		},
		Services: []Service{{
			Name: "sticky-edge",
			Type: "haproxy",
			// Re-encrypt to every replica's TLS port.
			Upstreams: []spec.Upstream{{Service: "sticky-backend", Port: 8443}},
			File:      "haproxy.cfg",
		}},
	},
	{
		ID:       "lb/weighted-canary",
		Title:    "Weighted canary with Traefik",
		Summary:  "A Traefik weighted service sends 90% of requests to v1 and 10% to the v2 canary; shift the weights to roll v2 out or back.",
		Language: "go",
		Template: "go/lb/weighted-canary",
		Dir:      "weighted-canary",
		Apps: []App{
			{Name: "canary-v1", Command: []string{"canary-v1"}, Replicas: 2},
			{Name: "canary-v2", Command: []string{"canary-v2"}},
			{Name: "canary-client", Command: []string{"canary-client"}, Uses: []string{"canary-edge"}},
		},
		Services: []Service{{
			Name:      "canary-edge",
			Type:      "traefik",
			Upstreams: []spec.Upstream{{Service: "canary-v1", Port: 8443}, {Service: "canary-v2", Port: 8443}},
			File:      "dynamic.yml",
		}},
	},
	{
		ID:       "lb/blue-green",
		Title:    "Blue-green deployment with NGINX",
		Summary:  "Blue serves while green waits; one variable and NGINX's graceful reload cut every request over to green, and back, without failing one.",
		Language: "go",
		Template: "go/lb/blue-green",
		Dir:      "blue-green",
		Apps: []App{
			{Name: "bg-blue", Command: []string{"bg-blue"}},
			{Name: "bg-green", Command: []string{"bg-green"}},
			{Name: "bg-client", Command: []string{"bg-client"}, Uses: []string{"bg-edge"}},
		},
		Services: []Service{{
			Name:      "bg-edge",
			Type:      "nginx",
			Upstreams: []spec.Upstream{{Service: "bg-blue", Port: 8443}, {Service: "bg-green", Port: 8443}},
			File:      "nginx.conf",
		}},
	},
	{
		ID:       "lb/health-checks",
		Title:    "Active and passive health checks",
		Summary:  "The same flaky replica behind HAProxy's active checks and Envoy's passive outlier detection, with the time each takes to eject it and to let it back.",
		Language: "go",
		Template: "go/lb/health-checks",
		Dir:      "health-checks",
		Apps: []App{
			{Name: "hc-backend", Command: []string{"hc-backend"}, Replicas: 3},
			{Name: "hc-client", Command: []string{"hc-client"}, Uses: []string{"hc-active", "hc-passive"}},
		},
		Services: []Service{
			{
				Name:      "hc-active",
				Type:      "haproxy",
				Upstreams: []spec.Upstream{{Service: "hc-backend", Port: 8443}},
				File:      "haproxy.cfg",
			},
			{
				Name:      "hc-passive",
				Type:      "envoy",
				Upstreams: []spec.Upstream{{Service: "hc-backend", Port: 8443}},
				File:      "envoy.yaml",
			},
		},
	},
	{
		ID:       "proxy/l4-vs-l7",
		Title:    "Layer 4 vs layer 7 proxying",
		Summary:  "One HAProxy, two listeners: mode http routes each request by path or header; mode tcp picks a server per connection and cannot see paths.",
		Language: "go",
		Template: "go/proxy/l4-vs-l7",
		Dir:      "l4-vs-l7",
		Apps: []App{
			{Name: "l4l7-api", Command: []string{"l4l7-api"}, Replicas: 2},
			{Name: "l4l7-web", Command: []string{"l4l7-web"}, Replicas: 2},
			{Name: "l4l7-client", Command: []string{"l4l7-client"}, Uses: []string{"l4l7-edge"}},
		},
		Services: []Service{{
			Name: "l4l7-edge",
			Type: "haproxy",
			Upstreams: []spec.Upstream{
				{Service: "l4l7-api", Port: 8443, Name: "api"},
				{Service: "l4l7-web", Port: 8443, Name: "web"},
			},
			File: "haproxy.cfg",
		}},
	},
	{
		ID:       "proxy/tls-modes",
		Title:    "TLS at the proxy: re-encrypt, terminate, passthrough",
		Summary:  "Three backends behind one HAProxy, one per TLS mode; the client logs which certificate it was shown on each.",
		Language: "go",
		Template: "go/proxy/tls-modes",
		Dir:      "tls-modes",
		Apps: []App{
			{Name: "tlsm-reencrypt", Command: []string{"tlsm-reencrypt"}},
			{Name: "tlsm-terminate", Command: []string{"tlsm-terminate"}},
			{Name: "tlsm-passthrough", Command: []string{"tlsm-passthrough"}},
			{Name: "tlsm-client", Command: []string{"tlsm-client"}, Uses: []string{"tlsm-edge"}},
		},
		Services: []Service{{
			Name: "tlsm-edge",
			Type: "haproxy",
			Upstreams: []spec.Upstream{
				{Service: "tlsm-reencrypt", Port: 8443, Name: "reencrypt"},
				{Service: "tlsm-terminate", Name: "terminate", TLS: spec.TLS{Mode: spec.TLSOff, Reason: "practice TLS termination at the proxy: tlsm-terminate has no TLS code"}},
				{Service: "tlsm-passthrough", Port: 8443, Name: "passthrough", TLS: spec.TLS{Mode: spec.TLSPassthrough}},
			},
			File: "haproxy.cfg",
		}},
	},
	{
		ID:       "proxy/request-mirroring",
		Title:    "Request mirroring with Envoy",
		Summary:  "Envoy copies every request to v2 and discards its answers: v2 is tried on real traffic while clients only ever see v1.",
		Language: "go",
		Template: "go/proxy/request-mirroring",
		Dir:      "request-mirroring",
		Apps: []App{
			{Name: "mirror-v1", Command: []string{"mirror-v1"}},
			{Name: "mirror-v2", Command: []string{"mirror-v2"}},
			{Name: "mirror-client", Command: []string{"mirror-client"}, Uses: []string{"mirror-edge"}},
		},
		Services: []Service{{
			Name: "mirror-edge",
			Type: "envoy",
			Upstreams: []spec.Upstream{
				{Service: "mirror-v1", Port: 8443, Name: "v1"},
				{Service: "mirror-v2", Port: 8443, Name: "v2"},
			},
			File: "envoy.yaml",
		}},
	},
	{
		ID:       "proxy/connection-draining",
		Title:    "Connection draining with HAProxy",
		Summary:  "Drain a replica through HAProxy's runtime API, or let it fail its health check on SIGTERM, and stop it under load with no failed requests.",
		Language: "go",
		Template: "go/proxy/connection-draining",
		Dir:      "connection-draining",
		Apps: []App{
			{Name: "drain-backend", Command: []string{"drain-backend"}, Replicas: 3},
			{Name: "drain-client", Command: []string{"drain-client"}, Uses: []string{"drain-edge"}},
		},
		Services: []Service{{
			Name:      "drain-edge",
			Type:      "haproxy",
			Upstreams: []spec.Upstream{{Service: "drain-backend", Port: 8443, Name: "app"}},
			File:      "haproxy.cfg",
		}},
	},
	{
		ID:       "pgbouncer/pooling-modes",
		Title:    "PgBouncer pooling modes",
		Summary:  "A shared PgBouncer with a session pool and a transaction pool: server connections vs clients, and the session-state traps of transaction mode.",
		Language: "go",
		Template: "go/pgbouncer/pooling-modes",
		Dir:      "pooling-modes",
		Requires: []string{"postgres"},
		Apps: []App{
			{Name: "pool-modes", Command: []string{"pool-modes"}, Uses: []string{"pool-central"}},
		},
		Services: []Service{{
			Name:      "pool-central",
			Type:      "pgbouncer",
			Upstreams: []spec.Upstream{{Service: "postgres"}},
			File:      "pgbouncer.ini",
		}},
	},
	{
		ID:       "gw/path-host-routing",
		Title:    "Path and Host routing with Traefik",
		Summary:  "One Traefik gateway in front of four services: routes by path prefix (one with its prefix stripped) and by Host header, and a default route for everything else.",
		Language: "go",
		Template: "go/gw/path-host-routing",
		Dir:      "path-host-routing",
		Apps: []App{
			{Name: "route-users", Command: []string{"route-users"}},
			{Name: "route-orders", Command: []string{"route-orders"}},
			{Name: "route-admin", Command: []string{"route-admin"}},
			{Name: "route-default", Command: []string{"route-default"}},
			{Name: "route-client", Command: []string{"route-client"}, Uses: []string{"route-edge"}},
		},
		Services: []Service{{
			Name: "route-edge",
			Type: "traefik",
			// Each upstream's name is its app's, so the explorers tag the
			// Traefik service of the same name in dynamic.yml. The routes
			// themselves (by path and Host) are the file's.
			Upstreams: []spec.Upstream{
				{Service: "route-users", Port: 8443},
				{Service: "route-orders", Port: 8443},
				{Service: "route-admin", Port: 8443},
				{Service: "route-default", Port: 8443},
			},
			File: "dynamic.yml",
		}},
	},
	{
		ID:       "gw/request-transformation",
		Title:    "Request transformation with Traefik middlewares",
		Summary:  "Traefik middlewares add and remove headers both ways, strip and rewrite paths, and redirect retired URLs; an echo backend shows what arrived.",
		Language: "go",
		Template: "go/gw/request-transformation",
		Dir:      "request-transformation",
		Apps: []App{
			{Name: "xform-backend", Command: []string{"xform-backend"}},
			{Name: "xform-client", Command: []string{"xform-client"}, Uses: []string{"xform-edge"}},
		},
		Services: []Service{{
			Name:      "xform-edge",
			Type:      "traefik",
			Upstreams: []spec.Upstream{{Service: "xform-backend", Port: 8443}},
			File:      "dynamic.yml",
		}},
	},
	{
		ID:       "gw/api-versioning",
		Title:    "API versioning with NGINX",
		Summary:  "v1 and v2 of an API behind one NGINX, chosen by path or by an Api-Version header; the gateway marks every v1 answer deprecated with Deprecation, Sunset and Link headers.",
		Language: "go",
		Template: "go/gw/api-versioning",
		Dir:      "api-versioning",
		Apps: []App{
			{Name: "ver-v1", Command: []string{"ver-v1"}},
			{Name: "ver-v2", Command: []string{"ver-v2"}},
			{Name: "ver-client", Command: []string{"ver-client"}, Uses: []string{"ver-edge"}},
		},
		Services: []Service{{
			Name: "ver-edge",
			Type: "nginx",
			// Named v1 and v2, the routes the path prefixes give them
			// (/v1/, /v2/), so service details match the file.
			Upstreams: []spec.Upstream{
				{Service: "ver-v1", Port: 8443, Name: "v1"},
				{Service: "ver-v2", Port: 8443, Name: "v2"},
			},
			File: "nginx.conf",
		}},
	},
	{
		ID:       "gw/rate-limiting",
		Title:    "Rate limiting: token bucket and leaky bucket",
		Summary:  "The same bursts through Envoy's local_ratelimit (a token bucket that passes a burst at once) and NGINX limit_req (a leaky bucket that queues it), with a bucket per client.",
		Language: "go",
		Template: "go/gw/rate-limiting",
		Dir:      "rate-limiting",
		Apps: []App{
			{Name: "rate-backend", Command: []string{"rate-backend"}},
			{Name: "rate-client", Command: []string{"rate-client"}, Uses: []string{"rate-token", "rate-leaky"}},
		},
		Services: []Service{
			{
				Name:      "rate-token",
				Type:      "envoy",
				Upstreams: []spec.Upstream{{Service: "rate-backend", Port: 8443}},
				File:      "envoy.yaml",
			},
			{
				Name:      "rate-leaky",
				Type:      "nginx",
				Upstreams: []spec.Upstream{{Service: "rate-backend", Port: 8443}},
				File:      "nginx.conf",
			},
		},
	},
	{
		ID:       "gw/circuit-breaking",
		Title:    "Circuit breaking with Envoy",
		Summary:  "Envoy's cluster circuit breakers cap what reaches a slow backend: past max_requests and max_pending_requests, requests fail at once with 503 instead of piling up.",
		Language: "go",
		Template: "go/gw/circuit-breaking",
		Dir:      "circuit-breaking",
		Apps: []App{
			{Name: "cb-backend", Command: []string{"cb-backend"}},
			{Name: "cb-client", Command: []string{"cb-client"}, Uses: []string{"cb-edge"}},
		},
		Services: []Service{{
			Name:      "cb-edge",
			Type:      "envoy",
			Upstreams: []spec.Upstream{{Service: "cb-backend", Port: 8443}},
			File:      "envoy.yaml",
		}},
	},
	{
		ID:       "gw/retries-and-timeouts",
		Title:    "Retries, timeouts and retry budgets with Envoy",
		Summary:  "Envoy retries with per-try timeouts turn a flaky backend's 60% into nearly 100%; against a backend that is down, the same retries quadruple its load unless a retry budget bounds them.",
		Language: "go",
		Template: "go/gw/retries-and-timeouts",
		Dir:      "retries-and-timeouts",
		Apps: []App{
			{Name: "retry-flaky", Command: []string{"retry-flaky"}},
			{Name: "retry-down", Command: []string{"retry-down"}},
			{Name: "retry-client", Command: []string{"retry-client"}, Uses: []string{"retry-edge"}},
		},
		Services: []Service{{
			Name: "retry-edge",
			Type: "envoy",
			Upstreams: []spec.Upstream{
				{Service: "retry-flaky", Port: 8443, Name: "flaky"},
				{Service: "retry-down", Port: 8443, Name: "down"},
			},
			File: "envoy.yaml",
		}},
	},
	{
		ID:       "gw/jwt-auth",
		Title:    "JWT authentication at the edge with Envoy",
		Summary:  "Envoy verifies JWTs against the issuer's JWKS before any request reaches the backend: no token or a forged signature gets 401, a valid token arrives as trusted claim headers.",
		Language: "go",
		Template: "go/gw/jwt-auth",
		Dir:      "jwt-auth",
		Apps: []App{
			{Name: "jwt-issuer", Command: []string{"jwt-issuer"}},
			{Name: "jwt-backend", Command: []string{"jwt-backend"}},
			{Name: "jwt-client", Command: []string{"jwt-client"}, Uses: []string{"jwt-edge"}},
		},
		Services: []Service{{
			Name: "jwt-edge",
			Type: "envoy",
			Upstreams: []spec.Upstream{
				{Service: "jwt-backend", Port: 8443, Name: "backend"},
				{Service: "jwt-issuer", Port: 8443, Name: "issuer"},
			},
			File: "envoy.yaml",
		}},
	},
	{
		ID:       "gw/forward-auth",
		Title:    "Forward authentication with Traefik",
		Summary:  "Traefik asks an auth service about every API request before forwarding it: refusals come back from the auth service as they are, and allowed requests carry the identity it returned as headers.",
		Language: "go",
		Template: "go/gw/forward-auth",
		Dir:      "forward-auth",
		Apps: []App{
			{Name: "fauth-auth", Command: []string{"fauth-auth"}},
			{Name: "fauth-backend", Command: []string{"fauth-backend"}},
			{Name: "fauth-client", Command: []string{"fauth-client"}, Uses: []string{"fauth-edge"}},
		},
		Services: []Service{{
			Name: "fauth-edge",
			Type: "traefik",
			// Named like the file's Traefik services, so the explorers tag
			// them. The forwardAuth call to fauth-auth's /verify is the
			// middleware's own connection, not a route.
			Upstreams: []spec.Upstream{
				{Service: "fauth-backend", Port: 8443, Name: "backend"},
				{Service: "fauth-auth", Port: 8443, Name: "auth"},
			},
			File: "dynamic.yml",
		}},
	},
	{
		ID:       "gw/bff-aggregation",
		Title:    "Backend for frontend behind Traefik",
		Summary:  "One call to a BFF fans out to two services at once and returns only what the screen needs, next to the same screen loaded through plain gateway routing: fewer round trips and fewer bytes.",
		Language: "go",
		Template: "go/gw/bff-aggregation",
		Dir:      "bff-aggregation",
		Apps: []App{
			{Name: "bff-api", Command: []string{"bff-api"}},
			{Name: "bff-profile", Command: []string{"bff-profile"}},
			{Name: "bff-orders", Command: []string{"bff-orders"}},
			{Name: "bff-client", Command: []string{"bff-client"}, Uses: []string{"bff-edge"}},
		},
		Services: []Service{{
			Name: "bff-edge",
			Type: "traefik",
			// Named like the file's Traefik services, so the explorers tag them.
			Upstreams: []spec.Upstream{
				{Service: "bff-api", Port: 8443, Name: "bff"},
				{Service: "bff-profile", Port: 8443, Name: "profile"},
				{Service: "bff-orders", Port: 8443, Name: "orders"},
			},
			File: "dynamic.yml",
		}},
	},
	{
		ID:       "gw/grpc-routing",
		Title:    "gRPC routing through Envoy",
		Summary:  "One Envoy listener routes gRPC calls to two services by their service name over HTTP/2, balances calls across replicas, and cuts one slow method off with DEADLINE_EXCEEDED.",
		Language: "go",
		Template: "go/gw/grpc-routing",
		Dir:      "grpc-routing",
		Apps: []App{
			{Name: "grpc-catalog", Command: []string{"grpc-catalog"}, Replicas: 2},
			{Name: "grpc-inventory", Command: []string{"grpc-inventory"}},
			{Name: "grpc-client", Command: []string{"grpc-client"}, Uses: []string{"grpc-edge"}},
		},
		Services: []Service{{
			Name: "grpc-edge",
			Type: "envoy",
			// Named like the file's clusters, so the explorers tag them.
			Upstreams: []spec.Upstream{
				{Service: "grpc-catalog", Port: 8443, Name: "catalog"},
				{Service: "grpc-inventory", Port: 8443, Name: "inventory"},
			},
			File: "envoy.yaml",
		}},
	},
}

// envoyInbound is an Envoy sidecar terminating TLS on 8443 in front of an
// app listening on 127.0.0.1:8080.
func envoyInbound() spec.Sidecar {
	return spec.Sidecar{Type: spec.SidecarEnvoy, Envoy: &spec.EnvoySidecar{Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080}}}
}

// envoyOutbound is an Envoy sidecar with one ambassador listener.
func envoyOutbound(o spec.EnvoyOutbound) spec.Sidecar {
	return spec.Sidecar{Type: spec.SidecarEnvoy, Envoy: &spec.EnvoySidecar{Outbound: []spec.EnvoyOutbound{o}}}
}

func intPtr(n int) *int { return &n }

// All returns every starter.
func All() []Starter { return slices.Clone(all) }

// Lookup finds a starter by id.
func Lookup(id string) (Starter, bool) {
	i := slices.IndexFunc(all, func(s Starter) bool { return s.ID == id })
	if i < 0 {
		return Starter{}, false
	}
	return all[i], true
}

// Lab is the experiment a starter is rendered for.
type Lab struct {
	Experiment string
	// Domain is the install's service domain (default bb.internal).
	Domain string
	// Infra maps each catalog type in the starter's Requires to the
	// experiment's service of that type.
	Infra map[string]Infra
}

// Infra is the experiment's service for one catalog type a starter
// requires.
type Infra struct {
	Name string
	// Config is every config option of the service, defaults applied.
	Config map[string]string
}

// DefaultLab is the lab s renders for in an experiment that has none of
// its required services yet: each type gets a service named after it with
// the catalog's default config, as applying the starter would add.
func (s Starter) DefaultLab(experiment, domain string) Lab {
	lab := Lab{Experiment: experiment, Domain: domain, Infra: map[string]Infra{}}
	for _, typ := range s.Requires {
		lab.Infra[typ] = DefaultInfra(typ)
	}
	return lab
}

// DefaultInfra is a service named after a catalog type, with the type's
// default config.
func DefaultInfra(typ string) Infra {
	in := Infra{Name: typ, Config: map[string]string{}}
	if e, ok := catalog.Lookup(typ); ok {
		in.Config = e.ResolveConfig(nil)
	}
	return in
}

// Data is what templates can use.
type Data struct {
	Experiment string
	// Domain is the install's service domain (default bb.internal): every
	// service and app instance is <name>.<Domain>, which native proxy
	// configs need for their backends.
	Domain        string
	GoVersion     string
	GoImage       string
	AlpineVersion string

	infra map[string]Infra
}

// Service is the name of the experiment's service of a catalog type the
// starter requires: {{.Service "postgres"}}.
func (d Data) Service(typ string) (string, error) {
	in, ok := d.infra[typ]
	if !ok {
		return "", fmt.Errorf("the template names a %s service, which the starter does not require", typ)
	}
	return in.Name, nil
}

// Config is one config option, defaults applied, of the experiment's
// service of a required catalog type: {{.Config "postgres" "database"}}.
func (d Data) Config(typ, key string) (string, error) {
	in, ok := d.infra[typ]
	if !ok {
		return "", fmt.Errorf("the template names a %s service, which the starter does not require", typ)
	}
	v, ok := in.Config[key]
	if !ok {
		return "", fmt.Errorf("the %s service has no config option %q", typ, key)
	}
	return v, nil
}

// File is one rendered file, relative to the starter's directory.
type File struct {
	Path string
	Data []byte
}

// Render renders every template file of s for a lab.
func Render(s Starter, lab Lab) ([]File, error) {
	return RenderFS(templates.FS, s, lab)
}

// RenderFS renders s from templates in fsys, rooted like the embedded
// templates. Tests use it to apply starters that do not ship.
func RenderFS(fsys fs.FS, s Starter, lab Lab) ([]File, error) {
	for _, typ := range s.Requires {
		if _, ok := lab.Infra[typ]; !ok {
			return nil, fmt.Errorf("starter %s: the lab has no %s service", s.ID, typ)
		}
	}
	data := Data{Experiment: lab.Experiment, Domain: lab.Domain, GoVersion: GoVersion, GoImage: GoImage, AlpineVersion: AlpineVersion, infra: lab.Infra}
	var files []File
	err := fs.WalkDir(fsys, s.Template, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".tmpl") {
			return fmt.Errorf("starter %s: %s is missing the .tmpl suffix", s.ID, p)
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		t, err := template.New(path.Base(p)).Option("missingkey=error").Parse(string(src))
		if err != nil {
			return fmt.Errorf("starter %s: %w", s.ID, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return fmt.Errorf("starter %s: %w", s.ID, err)
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p, s.Template+"/"), ".tmpl")
		files = append(files, File{Path: rel, Data: buf.Bytes()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, svc := range s.Services {
		for _, f := range []struct{ key, path string }{{"file", svc.File}, {"static_file", svc.StaticFile}} {
			if f.path != "" && !slices.ContainsFunc(files, func(tf File) bool { return tf.Path == f.path }) {
				return nil, fmt.Errorf("starter %s: service %s: %s %s is not in the template (add %s.tmpl)", s.ID, svc.Name, f.key, f.path, f.path)
			}
		}
	}
	return files, nil
}
