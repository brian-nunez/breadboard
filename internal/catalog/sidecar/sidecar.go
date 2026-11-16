// Package sidecar is the sidecar catalog: container types that run inside
// an app's pod, sharing its network namespace (same address, same
// localhost) and a directory with it. It is separate from the infra catalog
// in internal/catalog because a sidecar is never a service of its own: it
// belongs to one app instance and presents that instance's certificate.
//
// Adding a sidecar type means adding one Type here (with its Render) and
// its spec shape in internal/spec.
package sidecar

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// Paths and images shared by every pod.
const (
	// SharedDir is mounted in the app and every sidecar of a pod; the app
	// receives it as BB_SHARED_DIR.
	SharedDir = "/var/run/bb"
	// LogDir is where an app writes log files for a log shipper.
	LogDir = SharedDir + "/logs"
	// PodImage runs the namespace holder (and is the base of init images).
	PodImage = "alpine:3.24.4"
)

// PodResources are the namespace holder's limits: it only sleeps.
var PodResources = catalog.Resources{Memory: "16m", CPUs: "0.05"}

// Type is one sidecar type.
type Type struct {
	ID      string
	Title   string
	Summary string
	// Image is an exact version pin. Empty for consul-agent, which runs
	// its registry's image.
	Image     string
	Resources catalog.Resources
	// Internal types are created by the engine (register) and cannot be
	// declared in sidecars.
	Internal bool
	// Render returns the sidecar's container and what it changes in the
	// app's environment. Nil means the type is not built yet.
	Render func(Input) (Output, error)
}

// Input is everything a sidecar type needs to render one sidecar.
type Input struct {
	// Name is the sidecar's compose service name, e.g. api-envoy or
	// api-2-envoy. Generated files are written under config/<Name>/.
	Name   string
	Domain string
	App    App
	// Spec is the declared entry with defaults applied; zero for consul-agent.
	Spec spec.Sidecar
	// Targets are an Envoy sidecar's outbound targets, in spec order.
	Targets []Target
	// Uses are the services the app uses, with the variable names the app
	// receives for each (a second service of a type gets prefixed names).
	Uses []Use
	// Secret returns a reference to one of a service's secrets: the value,
	// or a ${VAR} reference resolved from a .env file.
	Secret func(service string) func(key string) string
	// Register is set for consul-agent.
	Register *Register
	// Siblings are the types of the other sidecars in the same pod,
	// including consul-agent when the app registers.
	Siblings []string
	// UserFile is the content of an Envoy sidecar's file, read from the
	// experiment directory; nil when rendering only the app's environment.
	UserFile []byte
	// Transparent is set when the pod captures the app's traffic, so its
	// sidecars run as the users the rules exempt.
	Transparent bool
	// AppTLSPorts are the ports the app itself serves TLS on, besides an
	// Envoy inbound listener: its register.port. In transparent mode they
	// are the only app ports reachable from outside the pod.
	AppTLSPorts []int
}

// App is the app instance whose pod the sidecar runs in.
type App struct {
	Service    string // the app container's service name: api, or api-2 for a replica
	Name       string // the app's name in experiment.yaml
	FQDN       string // the instance's own name, first on its certificate
	SharedFQDN string // the app's shared name, on every replica's certificate
	Instance   int    // 1…N for replicas; 0 for a single instance
}

// Target is one Envoy outbound target, resolved.
type Target struct {
	Service  string // a service or app name
	App      bool   // the target is an app (an app-to-app edge)
	FQDN     string // its shared name: Envoy balances across every replica
	Port     int    // the TLS port Envoy connects to
	Outbound spec.EnvoyOutbound
	// Addresses are the target's container addresses (every replica's),
	// known when the lab has static addressing; egress deny allows them.
	Addresses []string
}

// Use is one service the app uses.
type Use struct {
	Service string
	Type    string // catalog id
	FQDN    string
	Port    int // the listener port of the edge
	TLS     bool
	Config  map[string]string
	Env     []catalog.EnvVar // the variables the app receives for it, final names
	// Addresses are the service's address on the edge (its private
	// segment address for a plaintext edge), known when the lab has
	// static addressing; egress deny allows them.
	Addresses []string
}

// Register is what a consul-agent registers and where.
type Register struct {
	Entry           *catalog.Entry // the registry's catalog entry
	RegistryService string         // the registry's service name
	RegistryFQDN    string
	Service         string // the registered service name
	Port            int
	Check           string // HTTPS check path; empty means a TCP check
}

// Output is a rendered sidecar.
type Output struct {
	// Container holds the command, environment, generated files (written
	// under config/<Input.Name>/) and health check.
	catalog.Container
	// AppEnv changes the app's environment: a variable whose name the app
	// already has replaces its value in place (e.g. DATABASE_URL pointed at
	// a local pooler); new names are added (e.g. BB_LISTEN_ADDR, API_URL).
	AppEnv []catalog.EnvVar
	// Rewrites names the used service whose variables AppEnv may replace
	// (a pooler's own Postgres service). Any other name AppEnv shares with
	// the app's environment is a collision.
	Rewrites string
	// Needs lists infra services that must be healthy before the sidecar starts.
	Needs []string
	// Init is a one-shot container run in the pod before any sidecar starts.
	Init *Init
}

// Init is a one-shot container that prepares the pod, such as transparent
// mode's traffic rules. Compose builds its image from Dockerfile, grants it
// CapAdd (the only container that may hold NET_ADMIN), runs it with restart
// "no", and makes every sidecar wait for it to complete successfully.
type Init struct {
	Dockerfile []byte
	Command    []string
	CapAdd     []string
	Resources  catalog.Resources
}

var types = []*Type{envoy(), pgbouncer(), fluentBit(), consulAgent()}

// All returns every type, in display order.
func All() []*Type { return slices.Clone(types) }

// Lookup finds a type by id.
func Lookup(id string) (*Type, bool) {
	i := slices.IndexFunc(types, func(t *Type) bool { return t.ID == id })
	if i < 0 {
		return nil, false
	}
	return types[i], true
}

// RenderSidecar renders one sidecar, failing clearly for a type not built yet.
func (t *Type) RenderSidecar(in Input) (Output, error) {
	if t.Render == nil {
		return Output{}, fmt.Errorf("sidecar type %s is not implemented yet", t.ID)
	}
	return t.Render(in)
}

func envoy() *Type {
	return &Type{
		ID:        spec.SidecarEnvoy,
		Title:     "Envoy",
		Summary:   "Envoy proxy beside the app: TLS termination in front of it, TLS origination for its calls, optional transparent capture.",
		Image:     "envoyproxy/envoy:v1.40.2",
		Resources: catalog.Resources{Memory: "128m", CPUs: "0.25"},
		Render:    renderEnvoy,
	}
}

func pgbouncer() *Type {
	return &Type{
		ID:        spec.SidecarPgBouncer,
		Title:     "PgBouncer",
		Summary:   "PgBouncer connection pool on loopback, connecting to Postgres over verified TLS.",
		Image:     "edoburu/pgbouncer:v1.26.1-p0",
		Resources: catalog.Resources{Memory: "64m", CPUs: "0.25"},
		Render:    renderPgBouncer,
	}
}

func fluentBit() *Type {
	return &Type{
		ID:        spec.SidecarFluentBit,
		Title:     "Fluent Bit",
		Summary:   "Fluent Bit tails the log files the app writes to the shared directory and ships them to an S3 compatible bucket.",
		Image:     "fluent/fluent-bit:5.2.0-debug",
		Resources: catalog.Resources{Memory: "64m", CPUs: "0.25"},
		Render:    renderFluentBit,
	}
}

// consulAgent is the registration agent behind register: a Consul client
// agent rendered by the registry's catalog entry.
func consulAgent() *Type {
	return &Type{
		ID:        spec.SidecarConsulAgent,
		Title:     "Consul agent",
		Summary:   "Consul client agent that registers the app with a health check and deregisters it when it stops.",
		Resources: catalog.Resources{Memory: "128m", CPUs: "0.25"},
		Internal:  true,
		Render: func(in Input) (Output, error) {
			r := in.Register
			if r == nil || r.Entry == nil || r.Entry.Sidecar == nil {
				return Output{}, fmt.Errorf("sidecar %s: no service registry to register with", in.Name)
			}
			c := r.Entry.Sidecar(catalog.Sidecar{
				App:      in.App.Service,
				AppFQDN:  in.App.FQDN,
				Registry: r.RegistryFQDN,
				Secret:   in.Secret(r.RegistryService),
				Service:  r.Service,
				Port:     r.Port,
				Check:    r.Check,
			})
			if in.Transparent {
				// The rules exempt this reserved user, not the image's own.
				uid := strconv.Itoa(TransparentAgentUID)
				c.Environment = append(c.Environment, catalog.EnvVar{Name: "CONSUL_UID", Value: uid}, catalog.EnvVar{Name: "CONSUL_GID", Value: uid})
			}
			return Output{Container: c, Needs: []string{r.RegistryService}}, nil
		},
	}
}
