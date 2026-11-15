// Package catalog defines Breadboard's infrastructure service types. Each
// entry is everything the engine knows about one type: image, listeners,
// server configuration with TLS on, secrets, health check, resource defaults,
// the env contract injected into apps, and learning notes.
//
// Adding a service type means adding one entry file here and nothing else.
package catalog

import (
	"embed"
	"fmt"
	"slices"
	"time"

	"github.com/brian-nunez/breadboard/internal/spec"
)

// Paths inside every container. The compose materializer mounts files here;
// entries refer to them in server configuration and health checks.
const (
	TLSDir          = "/etc/bb/tls"
	CertFile        = TLSDir + "/tls.crt"
	KeyFile         = TLSDir + "/tls.key"
	KeystoreFile    = TLSDir + "/keystore.pem" // key followed by certificate, for servers that want one file
	CAFile          = "/etc/bb/ca.pem"
	TrustBundleFile = "/etc/bb/trust-bundle.pem"
	ConfigDir       = "/etc/bb/config"
)

// Entry is one infrastructure service type.
type Entry struct {
	ID      string
	Title   string
	Summary string
	// Image is an exact version pin. This field is the source of truth for versions.
	Image     string
	Listeners []Listener
	Resources Resources
	Secrets   []Secret
	Config    []ConfigOption
	// DataPath is where the service's named data volume is mounted.
	DataPath string
	// Notes is the embedded learning-notes markdown.
	Notes string
	// Server renders the container definition.
	Server func(Server) Container
	// Client renders the env contract for one app that uses the service.
	Client func(Client) []EnvVar
}

// Listener is a port the service accepts connections on.
type Listener struct {
	Name     string
	Protocol string
	Port     int
	// PlaintextPort is the port of the plaintext listener an edge can ask for
	// with tls.mode: off. Zero means the entry has none in v1.
	PlaintextPort int
}

// Resources are default limits.
type Resources struct {
	Memory string
	CPUs   string
}

// SecretKind selects how a secret is generated.
type SecretKind int

const (
	// Password is 32 base32 characters (A-Z, 2-7): safe in URLs, shells, and compose files.
	Password SecretKind = iota
	// ClusterID is a 16-byte random value, base64url without padding (Kafka's format).
	ClusterID
	// GossipKey is a 32-byte random value, standard base64 (Consul's gossip encryption key).
	GossipKey
)

// Secret is a generated value the service needs.
type Secret struct {
	Key  string
	Kind SecretKind
	Help string
}

// ConfigOption is a type-specific `config` key a spec may set.
type ConfigOption struct {
	Key     string
	Default string
	Help    string
	// Validate rejects a bad value with a message for the user.
	Validate func(string) error
}

// Server is the input for rendering a service's container.
type Server struct {
	Name   string
	FQDN   string
	Domain string
	Config map[string]string // every option, defaults applied
	// Secret returns a reference to one of this service's secrets: the value
	// itself, or a ${VAR} reference resolved from a .env file.
	Secret func(key string) string
	// Plain is set when at least one edge asked for plaintext.
	Plain *Plain
}

// Plain describes the private segment a plaintext listener lives on.
type Plain struct {
	FQDN    string // the service's alias on the segment
	Address string // the service's static IP on the segment
	Subnet  string // the segment's CIDR
	Clients []string
	Reasons []string
}

// Client is the input for rendering one app's env contract.
type Client struct {
	Service string
	FQDN    string
	Config  map[string]string
	Secret  func(key string) string
	// Plain is set when this edge is plaintext.
	Plain *Plain
}

// Container is a rendered service definition.
type Container struct {
	Entrypoint  []string
	Command     []string
	Environment []EnvVar
	Files       []File
	Health      Health
}

// File is generated configuration mounted read-only into the container.
type File struct {
	Name  string // written under the service's config directory
	Mount string // container path; empty means ConfigDir/Name
	Data  []byte
	Mode  uint32 // file mode; zero means 0644
}

// MountPath returns where the file appears in the container.
func (f File) MountPath() string {
	if f.Mount != "" {
		return f.Mount
	}
	return ConfigDir + "/" + f.Name
}

// Health is a container health check. Literal dollar signs in Test are
// written $$ because compose interpolates the file.
type Health struct {
	Test          []string
	Interval      time.Duration
	Timeout       time.Duration
	Retries       int
	StartPeriod   time.Duration
	StartInterval time.Duration
}

// EnvVar is one environment variable.
type EnvVar struct {
	Name  string
	Value string
	// Secret marks values that embed a secret, so displays can mask them.
	Secret bool
}

//go:embed notes/*.md
var notesFS embed.FS

func notes(id string) string {
	data, err := notesFS.ReadFile("notes/" + id + ".md")
	if err != nil {
		panic(fmt.Sprintf("catalog: missing notes for %s", id))
	}
	return string(data)
}

var entries = []*Entry{kafka(), postgres(), redis(), consul()}

// All returns every entry, in display order.
func All() []*Entry { return slices.Clone(entries) }

// Lookup finds an entry by id.
func Lookup(id string) (*Entry, bool) {
	i := slices.IndexFunc(entries, func(e *Entry) bool { return e.ID == id })
	if i < 0 {
		return nil, false
	}
	return entries[i], true
}

// SupportsPlaintext reports whether any listener can run without TLS.
func (e *Entry) SupportsPlaintext() bool {
	return slices.ContainsFunc(e.Listeners, func(l Listener) bool { return l.PlaintextPort != 0 })
}

// ResolveConfig applies defaults to a spec's config.
func (e *Entry) ResolveConfig(cfg map[string]string) map[string]string {
	out := map[string]string{}
	for _, o := range e.Config {
		out[o.Key] = o.Default
		if v, ok := cfg[o.Key]; ok {
			out[o.Key] = v
		}
	}
	return out
}

// CheckConfig validates one config key and value.
func (e *Entry) CheckConfig(key, value string) error {
	i := slices.IndexFunc(e.Config, func(o ConfigOption) bool { return o.Key == key })
	if i < 0 {
		known := make([]string, len(e.Config))
		for j, o := range e.Config {
			known[j] = o.Key
		}
		return fmt.Errorf("unknown option for %s (known: %v)", e.ID, known)
	}
	if v := e.Config[i].Validate; v != nil {
		return v(value)
	}
	return nil
}

// Types adapts the catalog for spec validation.
func Types(id string) (spec.TypeInfo, bool) {
	e, ok := Lookup(id)
	if !ok {
		return spec.TypeInfo{}, false
	}
	info := spec.TypeInfo{Plaintext: e.SupportsPlaintext()}
	if len(e.Config) > 0 {
		info.Config = e.CheckConfig
	}
	return info, true
}
