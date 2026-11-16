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
		if svc.File != "" && !slices.ContainsFunc(files, func(f File) bool { return f.Path == svc.File }) {
			return nil, fmt.Errorf("starter %s: service %s: file %s is not in the template (add %s.tmpl)", s.ID, svc.Name, svc.File, svc.File)
		}
	}
	return files, nil
}
