package spec

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/brian-nunez/bkit/gate"
)

// TypeInfo is what validation needs to know about a catalog entry.
type TypeInfo struct {
	// Config validates one config key and value. Nil means the type takes no config.
	Config func(key, value string) error
	// Plaintext reports whether the entry can enable a plaintext listener for an edge.
	Plaintext bool
	// Registry reports whether apps using this type can set register.
	Registry bool
	// HTTPS reports whether the main listener speaks HTTPS, so an Envoy
	// sidecar can proxy HTTP to it.
	HTTPS bool
	// HTTPSListener reports whether any of the entry's listeners speaks
	// HTTPS, so the host router can route to a service of the type.
	HTTPSListener bool
	// Env lists the variables an app using a service of the type receives
	// (over a plaintext edge when plain is set). Nil means unknown.
	Env func(service string, plain bool) []string
	// Proxy is set for proxy types, which take upstreams and file. Nil
	// means the type is not a proxy.
	Proxy *ProxyInfo
}

// ProxyInfo is what validation needs to know about a proxy type's
// upstreams. It is data on the catalog entry, so adding a proxy changes
// no validation code.
type ProxyInfo struct {
	// Modes are the TLS modes an upstream may use (on, off, passthrough).
	Modes []string
	// HTTP marks an L7 HTTP proxy: an upstream it decrypts (on, off) must
	// speak HTTP, so it is an app or an infra service with an HTTPS
	// listener. Passthrough is TLS at L4 and takes any TLS target.
	HTTP bool
	// Targets limits upstreams to these catalog types (pgbouncer:
	// postgres); apps are then not allowed. Nil allows apps and any type.
	Targets []string
	// MinUpstreams is the number of upstreams the type requires; zero means
	// none are (an HTTP proxy without upstreams answers 503).
	MinUpstreams int
	// MaxUpstreams caps the number of upstreams; zero means no cap.
	MaxUpstreams int
	// StaticFile reports whether the type takes static_file: a YAML
	// mapping merged into its generated static configuration.
	StaticFile bool
}

// TypeLookup finds a catalog entry by id.
type TypeLookup func(id string) (TypeInfo, bool)

// Error makes a Violation usable as a gate check result.
func (v Violation) Error() string { return v.String() }

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	memory   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([bkmg]|[kmg]b|[kmg]ib)?$`)
)

// Name rules, shared with the core for request validation.
var (
	IsDNSLabel     gate.Rule[string] = func(s string) bool { return dnsLabel.MatchString(s) }
	IsExperimentOK                   = IsDNSLabel.And(func(s string) bool { return len(s) <= 40 })
	IsServiceOK                      = IsDNSLabel.And(func(s string) bool { return len(s) <= 63 })
)

var (
	decimal = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

	isPositiveDecimal gate.Rule[string] = func(s string) bool {
		f, err := strconv.ParseFloat(s, 64)

		return decimal.MatchString(s) && err == nil && f > 0
	}
	// Docker treats a zero memory limit as no limit, so zero is rejected.
	isMemory gate.Rule[string] = func(s string) bool {
		s = strings.ToLower(s)
		if !memory.MatchString(s) {
			return false
		}

		f, err := strconv.ParseFloat(strings.TrimRight(s, "bkmgi"), 64)

		return err == nil && f > 0
	}
	isPort         gate.Rule[int]    = func(p int) bool { return p >= 1 && p <= 65535 }
	notEmpty       gate.Rule[string] = func(s string) bool { return strings.TrimSpace(s) != "" }
	isRelativePath gate.Rule[string] = func(s string) bool {
		return s != "" && !filepath.IsAbs(s) && !strings.HasPrefix(s, "~")
	}
	// A colon or comma would split the compose volume line; .bb/ is regenerated.
	isUsablePath gate.Rule[string] = func(s string) bool {
		clean := filepath.ToSlash(filepath.Clean(s))

		return !strings.ContainsAny(s, ":,\n") && clean != ".bb" && !strings.HasPrefix(clean, ".bb/")
	}
)

func check[T any](r gate.Rule[T], path, format string, args ...any) gate.Check[T] {
	return r.Else(Violation{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Validate applies every semantic rule to a decoded spec and returns a
// ValidationError listing all violations, or nil.
func Validate(s *Spec, types TypeLookup) error {
	var errs []error

	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	add(check(func(v int) bool { return v >= 1 }, "version", "must be %d or lower and at least 1", CurrentVersion)(s.Version))
	add(check(IsExperimentOK, "name", "must be a lowercase DNS label (a-z, 0-9, -) of at most 40 characters")(s.Name))

	names := map[string]string{}
	claim := func(name, path string) {
		if name == HostRouterName {
			add(Violation{Path: path, Message: fmt.Sprintf("name %s is reserved for the host router; choose another name", HostRouterName)})

			return
		}

		if prev, ok := names[name]; ok {
			add(Violation{Path: path, Message: fmt.Sprintf("name is already used by %s; services and apps share one namespace", prev)})

			return
		}

		names[name] = path
	}

	for _, svc := range s.Services {
		p := join("services", svc.Name)
		claim(svc.Name, p)
		add(check(IsServiceOK, p, "name must be a lowercase DNS label of at most 63 characters")(svc.Name))
		add(resourceChecks(join(p, "resources"))(svc.Resources))

		info, ok := types(svc.Type)
		if svc.Type == "" {
			add(Violation{Path: join(p, "type"), Message: "is required"})
		} else if !ok {
			add(Violation{Path: join(p, "type"), Message: fmt.Sprintf("unknown type %q; run bb catalog to list types", svc.Type)})
		}

		if !ok {
			continue
		}

		validateProxy(s, types, svc, info, p, add)
		validateServiceHost(svc, info, p, add)

		for k, v := range svc.Config {
			cp := join(join(p, "config"), k)
			if info.Config == nil {
				add(Violation{Path: cp, Message: fmt.Sprintf("type %s takes no config", svc.Type)})

				continue
			}

			if err := info.Config(k, v); err != nil {
				add(Violation{Path: cp, Message: err.Error()})
			}
		}
	}

	guarded := TerminateTargets(s)
	for _, app := range s.Apps {
		p := join("apps", app.Name)
		claim(app.Name, p)
		add(gate.Join(
			check(func(a App) bool { return IsServiceOK(a.Name) }, p, "name must be a lowercase DNS label of at most 63 characters"),
			check(func(a App) bool { return isRelativePath(a.Path) }, join(p, "path"), "must be a path relative to experiment.yaml"),
			check(func(a App) bool { return isUsablePath(a.Path) }, join(p, "path"), "must not contain a colon, comma, or newline, and must not point into .bb"),
		)(app))

		for i, c := range app.Command {
			add(check(notEmpty, fmt.Sprintf("%s.command[%d]", p, i), "must not be empty")(c))
		}

		add(resourceChecks(join(p, "resources"))(app.Resources))

		seenPorts := map[int]bool{}

		for i, port := range app.Ports {
			pp := fmt.Sprintf("%s.ports[%d]", p, i)
			add(check(isPort, pp, "must be between 1 and 65535")(port))

			if seenPorts[port] {
				add(Violation{Path: pp, Message: "duplicate port"})
			}

			seenPorts[port] = true
		}

		if app.Health != nil {
			hp := join(p, "health")
			if app.Health.Path != "" {
				add(check(func(s string) bool { return strings.HasPrefix(s, "/") }, join(hp, "path"), "must start with /")(app.Health.Path))
			}

			if app.Health.Port != 0 {
				add(check(isPort, join(hp, "port"), "must be between 1 and 65535")(app.Health.Port))
			}
		}

		validateInstances(app, p, guarded[app.Name], add, claim)
		validateSidecars(s, types, app, p, add)
		validateAppHost(app, p, add)

		if r := app.Register; r != nil {
			rp := join(p, "register")
			if r.Port != 0 {
				add(check(isPort, join(rp, "port"), "must be between 1 and 65535")(r.Port))
			}

			if r.Name != "" {
				add(check(IsServiceOK, join(rp, "name"), "must be a lowercase DNS label of at most 63 characters")(r.Name))
			}

			if r.Check != "" {
				add(check(func(s string) bool { return strings.HasPrefix(s, "/") }, join(rp, "check"), "must start with /")(r.Check))
			}

			registry := slices.ContainsFunc(app.Uses, func(u Use) bool {
				svc, ok := s.Service(u.Service)
				if !ok {
					return false
				}

				info, ok := types(svc.Type)

				return ok && info.Registry
			})
			if !registry {
				add(Violation{Path: rp, Message: "needs a service registry (consul) in uses"})
			}
		}

		seenUses := map[string]bool{}

		for i, u := range app.Uses {
			up := fmt.Sprintf("%s.uses[%d]", p, i)
			add(useChecks(s, types, up)(u))

			if seenUses[u.Service] {
				add(Violation{Path: up, Message: fmt.Sprintf("%s is listed more than once", u.Service)})
			}

			seenUses[u.Service] = true
		}
	}

	validatePlainPorts(s, add)

	return collect(errs)
}

func resourceChecks(path string) gate.Check[Resources] {
	return gate.Join(
		check(func(r Resources) bool { return r.Memory == "" || isMemory(r.Memory) }, join(path, "memory"), "must be a Docker size such as 512m or 1g"),
		check(func(r Resources) bool { return r.CPUs == "" || isPositiveDecimal(r.CPUs) }, join(path, "cpus"), "must be a positive decimal such as 0.5"),
	)
}

func useChecks(s *Spec, types TypeLookup, path string) gate.Check[Use] {
	target := func(u Use) (Service, bool) { return s.Service(u.Service) }
	exists := gate.Rule[Use](func(u Use) bool {
		_, ok := target(u)

		return ok
	})
	plaintext := gate.Rule[Use](func(u Use) bool { return u.TLS.Plaintext() })
	hasReason := gate.Rule[Use](func(u Use) bool { return notEmpty(u.TLS.Reason) })
	plaintextSupported := gate.Rule[Use](func(u Use) bool {
		svc, ok := target(u)
		if !ok {
			return true // reported by the exists check
		}

		info, ok := types(svc.Type)

		return !ok || info.Plaintext
	})

	return gate.Join(
		gate.First(
			check(func(u Use) bool { return u.Service != "" }, join(path, "service"), "is required"),
			exists.Else(Violation{Path: path, Message: "must name a service under services"}),
		),
		plaintext.Requires(hasReason).Else(Violation{Path: join(path, "tls.reason"), Message: "is required when tls.mode is off; say why this hop is plaintext"}),
		plaintext.Requires(plaintextSupported).Else(Violation{Path: join(path, "tls.mode"), Message: "this service type has no plaintext listener in v1"}),
	)
}

// collect flattens gate's joined errors into one ValidationError.
func collect(list []error) error {
	var (
		out  []Violation
		walk func(error)
	)

	walk = func(err error) {
		var v Violation
		switch {
		case err == nil:
		case errors.As(err, &v) && !isJoined(err):
			out = append(out, v)
		case isJoined(err):
			for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
				walk(e)
			}
		default:
			out = append(out, Violation{Message: err.Error()})
		}
	}
	for _, err := range list {
		walk(err)
	}

	return violations(out)
}

func isJoined(err error) bool {
	_, ok := err.(interface{ Unwrap() []error })

	return ok
}
