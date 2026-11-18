package spec

import (
	"strconv"
	"strings"
	"time"
)

// Sidecar types a spec may declare under apps.<name>.sidecars.
const (
	SidecarEnvoy     = "envoy"
	SidecarPgBouncer = "pgbouncer"
	SidecarFluentBit = "fluent-bit"
	// SidecarConsulAgent is internal: register creates it, and a spec
	// cannot declare it in sidecars.
	SidecarConsulAgent = "consul-agent"
)

// SidecarTypes lists the types a spec may declare, in display order.
var SidecarTypes = []string{SidecarEnvoy, SidecarPgBouncer, SidecarFluentBit}

// Ports Envoy sidecars bind inside the pod.
const (
	EnvoyAdminPort    = 15000 // admin API on 127.0.0.1: health and explorers
	EnvoyOutboundPort = 15001 // transparent mode: redirected outbound traffic
	EnvoyInboundPort  = 15006 // transparent mode: redirected inbound traffic
	// FluentBitHTTPPort is Fluent Bit's HTTP server (health) on 127.0.0.1.
	FluentBitHTTPPort = 2020
)

// consulAgentPorts are bound by a registering app's Consul client agent:
// Serf LAN gossip and the loopback HTTPS API. Kept in step with
// internal/catalog/consul.go.
var consulAgentPorts = []int{8301, 8501}

// Defaults for sidecar fields a spec leaves out.
const (
	DefaultPgBouncerPort     = 6432
	DefaultPgBouncerPoolMode = "transaction"
	DefaultPgBouncerPoolSize = 10
	DefaultEnvoyLB           = "round_robin"
	DefaultEnvoyTimeout      = "5s"
	DefaultEnvoyRetries      = 2
	DefaultEgress            = EgressAllow
	// Envoy's own outlier detection defaults (v1.40), written out so the
	// generated config shows them.
	DefaultOutlierMaxEjectionPercent = 10
	DefaultOutlierInterval           = "10s"
)

// MaxReplicas bounds apps.<name>.replicas.
const MaxReplicas = 10

// Egress policies for an Envoy sidecar in transparent mode.
const (
	EgressAllow = "allow"
	EgressDeny  = "deny"
)

var (
	envoyLBPolicies    = []string{"round_robin", "least_request", "random", "ring_hash", "maglev"}
	pgBouncerPoolModes = []string{"session", "transaction", "statement"}
	egressPolicies     = []string{EgressAllow, EgressDeny}
)

// Catalog types sidecars refer to.
const (
	typePostgres     = "postgres"
	typeS3Compatible = "s3-compatible"
)

// Sidecar is one entry of an app's sidecars list: a container that shares
// the app's network namespace and a directory with it. Exactly one of the
// type-specific fields is set, matching Type.
type Sidecar struct {
	Name string // as written; empty means the type
	Type string

	Envoy     *EnvoySidecar
	PgBouncer *PgBouncerSidecar
	FluentBit *FluentBitSidecar
}

// ID is the sidecar's name within its app: Name, or the type when unset.
func (s Sidecar) ID() string {
	if s.Name != "" {
		return s.Name
	}

	return s.Type
}

// EnvoySidecar terminates TLS in front of the app (inbound), proxies the
// app's plaintext loopback calls to TLS targets (outbound), and optionally
// captures all of the app's traffic (transparent).
type EnvoySidecar struct {
	Inbound     *EnvoyInbound
	Outbound    []EnvoyOutbound
	Transparent *Transparent // nil means off
	File        string       // user-owned native config replacing the generated one
}

// EnvoyInbound is TLS termination: Envoy listens on Port with the app's
// certificate and forwards to the app on 127.0.0.1:AppPort.
type EnvoyInbound struct {
	Port    int
	AppPort int
}

// EnvoyOutbound is an ambassador listener on 127.0.0.1:Port that proxies to
// Service over verified TLS.
type EnvoyOutbound struct {
	Service string
	Port    int
	LB      string // load-balancing policy; empty means round_robin
	Timeout string // Go duration; empty means 5s
	Retries *int   // nil means 2
	Outlier *Outlier
}

// Outlier is Envoy outlier detection: eject a host after Consecutive5xx
// errors for Ejection, never more than MaxEjectionPercent of the cluster
// at once. Interval is the analysis sweep that returns ejected hosts.
type Outlier struct {
	Consecutive5xx     int
	Ejection           string // Go duration
	MaxEjectionPercent int    // 1-100; zero means 10, Envoy's default
	Interval           string // Go duration; empty means 10s, Envoy's default
}

// Transparent captures the app's traffic with iptables rules installed by a
// short-lived init container.
type Transparent struct {
	Egress string // allow or deny undeclared destinations; empty means allow
}

// PgBouncerSidecar pools the app's connections to a Postgres service.
type PgBouncerSidecar struct {
	Service  string
	PoolMode string
	Port     int
	PoolSize int
}

// FluentBitSidecar ships log files the app writes to the shared directory
// to an S3 compatible bucket.
type FluentBitSidecar struct {
	To     string
	Bucket string
}

// WithDefaults returns the sidecar with every omitted field defaulted.
func (s Sidecar) WithDefaults() Sidecar {
	out := s
	if out.Name == "" {
		out.Name = out.Type
	}

	if e := s.Envoy; e != nil {
		c := *e

		c.Outbound = make([]EnvoyOutbound, len(e.Outbound))
		for i, o := range e.Outbound {
			if o.LB == "" {
				o.LB = DefaultEnvoyLB
			}

			if o.Timeout == "" {
				o.Timeout = DefaultEnvoyTimeout
			}

			if o.Retries == nil {
				r := DefaultEnvoyRetries
				o.Retries = &r
			}

			if o.Outlier != nil {
				l := *o.Outlier
				if l.MaxEjectionPercent == 0 {
					l.MaxEjectionPercent = DefaultOutlierMaxEjectionPercent
				}

				if l.Interval == "" {
					l.Interval = DefaultOutlierInterval
				}

				o.Outlier = &l
			}

			c.Outbound[i] = o
		}

		if e.Transparent != nil {
			t := *e.Transparent
			if t.Egress == "" {
				t.Egress = DefaultEgress
			}

			c.Transparent = &t
		}

		out.Envoy = &c
	}

	if p := s.PgBouncer; p != nil {
		c := *p
		if c.PoolMode == "" {
			c.PoolMode = DefaultPgBouncerPoolMode
		}

		if c.Port == 0 {
			c.Port = DefaultPgBouncerPort
		}

		if c.PoolSize == 0 {
			c.PoolSize = DefaultPgBouncerPoolSize
		}

		out.PgBouncer = &c
	}

	if f := s.FluentBit; f != nil {
		c := *f
		out.FluentBit = &c
	}

	return out
}

// Transparent reports whether the sidecar captures the app's traffic.
func (s Sidecar) Transparent() bool { return s.Envoy != nil && s.Envoy.Transparent != nil }

// ReplicaCount is the number of instances the app runs.
func (a App) ReplicaCount() int {
	if a.Replicas < 1 {
		return 1
	}

	return a.Replicas
}

// IsPod reports whether the app runs as a pod: a namespace holder plus the
// app and its sidecars. Apps with only register keep their own namespace.
func (a App) IsPod() bool { return len(a.Sidecars) > 0 }

// Instances returns the service names of the app's instances: the app's own
// name with one replica, else <app>-1 … <app>-N.
func (a App) Instances() []string {
	n := a.ReplicaCount()
	if n == 1 {
		return []string{a.Name}
	}

	out := make([]string, n)
	for i := range n {
		out[i] = a.Name + "-" + strconv.Itoa(i+1)
	}

	return out
}

// TLSPort is the port other containers reach the app on over TLS: its Envoy
// sidecar's inbound port, else the port it registers (which it serves TLS
// on with TLS_CERT_FILE and TLS_KEY_FILE). False when it declares neither.
func (a App) TLSPort() (int, bool) {
	for _, sc := range a.Sidecars {
		if sc.Envoy != nil && sc.Envoy.Inbound != nil && sc.Envoy.Inbound.Port != 0 {
			return sc.Envoy.Inbound.Port, true
		}
	}

	if a.Register != nil && a.Register.Port != 0 {
		return a.Register.Port, true
	}

	return 0, false
}

// TargetEnv is the variable an app receives for an Envoy outbound target:
// API_URL for api, ORDER_SERVICE_URL for order-service.
func TargetEnv(service string) string {
	return strings.ToUpper(strings.ReplaceAll(service, "-", "_")) + "_URL"
}

// PodName is the service name of an instance's namespace holder.
func PodName(instance string) string { return instance + "-pod" }

// SidecarServiceName is the service name of a sidecar in an instance's pod.
func SidecarServiceName(instance, id string) string { return instance + "-" + id }

// NetinitName is the service name of the init container that installs an
// instance's transparent-mode traffic rules.
func NetinitName(instance string) string { return instance + "-netinit" }

// positiveDuration reports whether s is a Go duration above zero.
func positiveDuration(s string) bool {
	d, err := time.ParseDuration(s)

	return err == nil && d > 0
}
