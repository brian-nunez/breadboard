package catalog

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/brian-nunez/breadboard/internal/spec"
)

// Ports every HTTP proxy listens on (the 8443/9443 convention).
const (
	// ProxyHTTPSPort is the TLS L7 listener, with the proxy's own
	// certificate. Apps that use the proxy get <NAME>_URL pointing here.
	ProxyHTTPSPort = 8443
	// ProxyPassthroughPort is the TLS L4 listener, routed by SNI without
	// decrypting; present only when an upstream is passthrough. Apps get
	// <NAME>_PASSTHROUGH_ADDR pointing here.
	ProxyPassthroughPort = 9443
)

// Proxy marks an entry as a proxy: a service with upstream edges to apps
// and other infra services, each with its own TLS mode. Validation reads
// it through spec.ProxyInfo; the compose materializer passes the entry the
// resolved upstreams in Server.Upstreams.
type Proxy struct {
	// Modes are the upstream TLS modes the proxy supports: spec.TLSOn
	// (re-encrypt), spec.TLSOff (terminate) and spec.TLSPassthrough.
	Modes []string
	// HTTP marks an L7 HTTP proxy: decrypted upstreams must speak HTTP.
	HTTP bool
	// Targets limits upstreams to these catalog types; nil allows apps and
	// any infra type.
	Targets []string
	// MinUpstreams is the number of upstreams the type needs to work at
	// all (pgbouncer: its one Postgres); zero means none are required.
	MinUpstreams int
	// MaxUpstreams caps the number of upstreams; zero means no cap.
	MaxUpstreams int
	// ConfigFile is the name (under ConfigDir) of the generated native
	// config that a spec's file replaces. Server must emit a File with it.
	ConfigFile string
	// StaticFile is the name (under ConfigDir) of the generated static
	// configuration that a spec's static_file extends; empty when the
	// type takes no static_file. MergeStatic renders it with
	// Server.UserStatic merged in, or refuses the user's file.
	StaticFile  string
	MergeStatic func(Server) ([]byte, error)
	// Unit names the part of the generated config that balances one
	// upstream (an HAProxy backend, an Envoy cluster, a Traefik service),
	// so explorers can tell which upstream a live counter belongs to; nil
	// when the proxy reports nothing per upstream.
	Unit func(Upstream) string
}

// Upstream is one resolved backend of a proxy, ready to render.
type Upstream struct {
	// Name is the route name: requests to /<Name>/ reach it when the proxy
	// has several L7 upstreams.
	Name string
	// Service is the target's name in experiment.yaml (an app or a service).
	Service string
	// App marks an app target, which may run several replicas.
	App bool
	// Mode is spec.TLSOn, spec.TLSOff or spec.TLSPassthrough.
	Mode string
	// Reason is why a terminate hop is plaintext.
	Reason string
	// Host is a DNS name that resolves to every instance on the hop's
	// network: the target's shared FQDN, or its segment alias
	// (<target>.plain.<domain>) for a terminate hop. Proxies with DNS
	// discovery (HAProxy server-template, Envoy STRICT_DNS, NGINX resolve)
	// use it.
	Host string
	// Servers are each instance's own name on the hop's network: every
	// replica's FQDN (or segment alias for terminate), in instance order.
	// Proxies without DNS multi-A discovery (Traefik) list these.
	Servers []string
	// ServerName is the name on the target's certificate: the TLS SNI and
	// verified name for re-encrypt, and the SNI clients send for
	// passthrough. It is the shared FQDN, which every replica's
	// certificate carries.
	ServerName string
	// Port is the target port: its TLS port, or for terminate its
	// plaintext port.
	Port int
	// Config is an infra target's resolved config (defaults applied); nil
	// for an app. A pooler reads the database and role it serves here.
	Config map[string]string
	// Secret returns a reference to one of an infra target's secrets, like
	// Server.Secret; nil for an app, and until the upstream is rendered
	// (WithSecrets).
	Secret func(key string) string
}

// WithSecrets returns a copy of ups whose infra targets carry their
// secret references from ref, for an entry that hands a target's
// credentials on (PgBouncer logs in to Postgres as the app role).
func WithSecrets(ups []Upstream, ref func(service string) func(key string) string) []Upstream {
	if len(ups) == 0 {
		return nil
	}

	out := slices.Clone(ups)
	for i := range out {
		if !out[i].App {
			out[i].Secret = ref(out[i].Service)
		}
	}

	return out
}

// Addr is Host:Port.
func (u Upstream) Addr() string { return u.Host + ":" + strconv.Itoa(u.Port) }

// Prefix is the upstream's path prefix with several L7 upstreams: /<Name>/.
func (u Upstream) Prefix() string { return "/" + u.Name + "/" }

// Passthrough reports whether the proxy forwards the client's TLS session
// to this upstream untouched.
func (u Upstream) Passthrough() bool { return u.Mode == spec.TLSPassthrough }

// L7 returns the upstreams the proxy decrypts (re-encrypt and terminate),
// in spec order. With one, it receives every request; with several, each
// gets its /<Name>/ prefix (stripped before forwarding) and the first is
// also the default route.
func L7(ups []Upstream) []Upstream {
	return slices.DeleteFunc(slices.Clone(ups), Upstream.Passthrough)
}

// Passthroughs returns the passthrough upstreams, routed by SNI on
// ProxyPassthroughPort.
func Passthroughs(ups []Upstream) []Upstream {
	return slices.DeleteFunc(slices.Clone(ups), func(u Upstream) bool { return !u.Passthrough() })
}

// Render renders the service's container: Server, with a proxy's file
// swapped in for its generated config and its static_file merged into
// its generated static configuration.
func (e *Entry) Render(s Server) (Container, error) {
	c := e.Server(s)
	if s.UserConfig == nil && s.UserStatic == nil {
		return c, nil
	}

	c.Files = slices.Clone(c.Files)
	swap := func(name, what string, data []byte) error {
		i := slices.IndexFunc(c.Files, func(f File) bool { return f.Name == name })
		if i < 0 {
			return fmt.Errorf("%s: type %s rendered no %s for its %s", s.Name, e.ID, name, what)
		}

		c.Files[i].Data = data

		return nil
	}

	if s.UserConfig != nil {
		if e.Proxy == nil || e.Proxy.ConfigFile == "" {
			return Container{}, fmt.Errorf("%s: type %s takes no file", s.Name, e.ID)
		}

		if err := swap(e.Proxy.ConfigFile, "file", s.UserConfig); err != nil {
			return Container{}, err
		}
	}

	if s.UserStatic != nil {
		if e.Proxy == nil || e.Proxy.StaticFile == "" || e.Proxy.MergeStatic == nil {
			return Container{}, fmt.Errorf("%s: type %s takes no static_file", s.Name, e.ID)
		}

		data, err := e.Proxy.MergeStatic(s)
		if err != nil {
			return Container{}, fmt.Errorf("%s: static_file %s: %w", s.Name, s.UserStatic.Path, err)
		}

		if err := swap(e.Proxy.StaticFile, "static_file", data); err != nil {
			return Container{}, err
		}
	}

	return c, nil
}

// EnvName turns a service name into a variable prefix: EDGE for edge,
// API_GATEWAY for api-gateway.
func EnvName(service string) string {
	return strings.ToUpper(strings.ReplaceAll(service, "-", "_"))
}

// ProxyClientEnv is the env contract of an HTTP proxy, named after the
// service so an app can use several proxies:
// <NAME>_URL=https://<fqdn>:8443, and <NAME>_PASSTHROUGH_ADDR=<fqdn>:9443
// when an upstream is passthrough. The app trusts the proxy through
// SSL_CERT_FILE, like any service.
func ProxyClientEnv(c Client) []EnvVar {
	name := EnvName(c.Service)

	out := []EnvVar{{Name: name + "_URL", Value: fmt.Sprintf("https://%s:%d", c.FQDN, ProxyHTTPSPort)}}
	if slices.ContainsFunc(c.Upstreams, Upstream.Passthrough) {
		out = append(out, EnvVar{Name: name + "_PASSTHROUGH_ADDR", Value: fmt.Sprintf("%s:%d", c.FQDN, ProxyPassthroughPort)})
	}

	return out
}

// Comment renders user-derived text (a tls.reason, …) as comment lines that
// cannot escape into the config around them: CR LF and lone CR become line
// breaks, every other control character (C0, DEL and C1), LS, PS and BOM
// becomes a space, and every line starts
// with prefix, the indentation plus the format's comment marker ("    # "
// for HAProxy and NGINX, "# " in YAML such as Envoy's). Lines are joined
// with "\n", without a trailing one. Every proxy entry must render user
// text through it.
func Comment(prefix, text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '\ufeff':
			return ' '
		}

		return r
	}, text)

	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(prefix+l, " \t")
	}

	return strings.Join(lines, "\n")
}

// ListenersFor lists the entry's listeners as they run with these
// upstreams: an HTTP proxy adds its passthrough listener on 9443 when an
// upstream is passthrough.
func (e *Entry) ListenersFor(ups []Upstream) []Listener {
	out := slices.Clone(e.Listeners)
	if e.Proxy != nil && e.Proxy.HTTP && len(Passthroughs(ups)) > 0 {
		out = append(out, Listener{Name: "passthrough", Protocol: "tls", Port: ProxyPassthroughPort})
	}

	return out
}

// HTTPProxyListeners are the listeners of an HTTP proxy as edges see them:
// apps that use the proxy connect to its TLS L7 port. The passthrough port
// is added by the proxy's config when an upstream asks for it.
func HTTPProxyListeners() []Listener {
	return []Listener{{Name: "https", Protocol: "https", Port: ProxyHTTPSPort}}
}
