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
}

// App is an app service the starter adds.
type App struct {
	Name string
	// Command runs the app: the binary the Dockerfile's dev stage built
	// from cmd/<name> into /usr/local/bin.
	Command []string
	// Uses lists catalog types; each resolves to the experiment's service of that type.
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

// Data is what templates can use.
type Data struct {
	Experiment    string
	GoVersion     string
	GoImage       string
	AlpineVersion string
}

// File is one rendered file, relative to the starter's directory.
type File struct {
	Path string
	Data []byte
}

// Render renders every template file of s.
func Render(s Starter, experiment string) ([]File, error) {
	data := Data{Experiment: experiment, GoVersion: GoVersion, GoImage: GoImage, AlpineVersion: AlpineVersion}
	var files []File
	err := fs.WalkDir(templates.FS, s.Template, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".tmpl") {
			return fmt.Errorf("starter %s: %s is missing the .tmpl suffix", s.ID, p)
		}
		src, err := fs.ReadFile(templates.FS, p)
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
	return files, nil
}
