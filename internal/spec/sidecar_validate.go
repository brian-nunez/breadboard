package spec

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/brian-nunez/bkit/gate"
)

// S3 bucket names: 3 to 63 lowercase letters, digits, dots and hyphens,
// starting and ending with a letter or digit.
var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// validateInstances checks replicas and claims every service name the app
// generates (instances, pods, sidecars, agents, init containers), so none
// collides with another service or app.
func validateInstances(app App, p string, guarded bool, add func(error), claim func(name, path string)) {
	rp := join(p, "replicas")
	add(check(func(n int) bool { return n >= 0 && n <= MaxReplicas }, rp, "must be between 1 and %d", MaxReplicas)(app.Replicas))
	if app.ReplicaCount() > 1 && len(app.Ports) > 0 {
		add(Violation{Path: join(p, "ports"), Message: "cannot be published by more than one replica; remove ports or set replicas to 1"})
	}
	if app.Replicas > MaxReplicas {
		return // the names below would only repeat the error
	}
	generated := func(name, path string) {
		if !IsServiceOK(name) {
			add(Violation{Path: path, Message: fmt.Sprintf("makes a service named %q, longer than 63 characters; shorten the names", name)})
		}
		if name == HostRouterName {
			add(Violation{Path: path, Message: fmt.Sprintf("makes a service named %q, which is reserved for the host router; rename the app or sidecar", name)})
			return
		}
		claim(name, path)
	}
	for _, inst := range app.Instances() {
		if inst != app.Name {
			generated(inst, rp)
		}
		if app.IsPod() {
			generated(PodName(inst), join(p, "sidecars"))
		} else if guarded {
			generated(PodName(inst), p)
		}
		if guarded && !slices.ContainsFunc(app.Sidecars, Sidecar.Transparent) {
			// The plaintext guard's init container. (A transparent app
			// cannot be a terminate target; that is reported elsewhere.)
			generated(NetinitName(inst), p)
		}
		for i, sc := range app.Sidecars {
			if sc.Type == "" || sidecarKeys[sc.Type] == nil || sc.Name != "" && !IsDNSLabel(sc.Name) {
				continue // reported by decoding or by the sidecar checks
			}
			sp := fmt.Sprintf("%s.sidecars[%d]", p, i)
			if prev := slices.IndexFunc(app.Sidecars[:i], func(o Sidecar) bool { return o.ID() == sc.ID() }); prev >= 0 {
				continue // reported as a duplicate
			}
			generated(SidecarServiceName(inst, sc.ID()), sp)
			if sc.Transparent() {
				generated(NetinitName(inst), join(sp, "transparent"))
			}
		}
		if app.Register != nil {
			// The agent is a service of its own and shares the namespace.
			generated(SidecarName(inst), join(p, "register"))
		}
	}
}

// validateSidecars applies the rules for an app's sidecars list.
func validateSidecars(s *Spec, types TypeLookup, app App, p string, add func(error)) {
	if len(app.Sidecars) == 0 {
		return
	}
	// Published ports are not claims: the pod holder publishes whichever
	// container in the pod listens, so publishing inbound.port is how a
	// TLS-terminated app is reached from the host.
	pod := newPortClaims(add)
	claimAppPorts(app, p, pod)
	if app.Register != nil {
		for _, port := range consulAgentPorts {
			pod.claim(port, "the registration agent", join(p, "register"))
		}
	}
	ids := map[string]bool{}
	outboundTargets := map[string]string{}
	pooled := map[string]bool{}
	for i, raw := range app.Sidecars {
		sp := fmt.Sprintf("%s.sidecars[%d]", p, i)
		if raw.Type == "" || sidecarKeys[raw.Type] == nil {
			continue // reported while decoding
		}
		sc := raw.WithDefaults()
		if raw.Name != "" {
			add(check(IsDNSLabel, join(sp, "name"), "must be a lowercase DNS label (a-z, 0-9, -)")(raw.Name))
		}
		if ids[sc.ID()] {
			add(Violation{Path: sp, Message: fmt.Sprintf("duplicate sidecar name %q in this app; set name to tell them apart", sc.ID())})
		}
		ids[sc.ID()] = true
		owner := fmt.Sprintf("sidecar %s", sc.ID())
		switch {
		case sc.Envoy != nil:
			validateEnvoy(s, types, app, sp, sc.Envoy, add, pod, owner, outboundTargets)
			if in := sc.Envoy.Inbound; in != nil && slices.Contains(app.Ports, in.AppPort) {
				add(Violation{Path: join(sp, "inbound.app_port"), Message: "is bound on 127.0.0.1 only and cannot be published; publish inbound.port instead"})
			}
		case sc.PgBouncer != nil:
			pb := sc.PgBouncer
			if pb.Service != "" {
				if pooled[pb.Service] {
					add(Violation{Path: join(sp, "service"), Message: fmt.Sprintf("%s already has a pgbouncer sidecar in this app", pb.Service)})
				}
				pooled[pb.Service] = true
				usedService(s, app, join(sp, "service"), pb.Service, typePostgres, "a postgres service", add)
			}
			add(check(func(m string) bool { return slices.Contains(pgBouncerPoolModes, m) }, join(sp, "pool_mode"), "must be one of %s", strings.Join(pgBouncerPoolModes, ", "))(pb.PoolMode))
			add(check(isPort, join(sp, "port"), "must be between 1 and 65535")(pb.Port))
			add(check(func(n int) bool { return n >= 1 && n <= 1000 }, join(sp, "pool_size"), "must be between 1 and 1000")(pb.PoolSize))
			pod.claim(pb.Port, owner, join(sp, "port"))
		case sc.FluentBit != nil:
			fb := sc.FluentBit
			if fb.To != "" {
				usedService(s, app, join(sp, "to"), fb.To, typeS3Compatible, "an s3-compatible service", add)
			}
			if fb.Bucket != "" {
				add(check(func(b string) bool { return bucketName.MatchString(b) }, join(sp, "bucket"), "must be an S3 bucket name: 3 to 63 lowercase letters, digits, dots and hyphens")(fb.Bucket))
			}
			pod.claim(FluentBitHTTPPort, owner, sp)
		}
	}
	validateAppEnv(s, types, app, p, add)
}

// appBaseEnv are the variables Breadboard gives every app (kept in step
// with internal/compose).
var appBaseEnv = []string{"SSL_CERT_FILE", "TLS_CERT_FILE", "TLS_KEY_FILE", "BB_SERVICE", "BB_FQDN", "BB_HEALTH_PORT", "BB_SHARED_DIR", "BB_PLAIN_LISTEN_ADDR"}

// validateAppEnv refuses a sidecar variable whose name the app already
// receives from somewhere else: from Breadboard, from a used service's
// contract, or from another sidecar. A PgBouncer rewrites its own
// service's variables by design and adds none, so it never collides.
func validateAppEnv(s *Spec, types TypeLookup, app App, p string, add func(error)) {
	owner := map[string]string{}
	for _, name := range appBaseEnv {
		owner[name] = "Breadboard (every app receives it)"
	}
	for i, u := range app.Uses {
		svc, ok := s.Service(u.Service)
		if !ok {
			continue
		}
		info, ok := types(svc.Type)
		if !ok || info.Env == nil {
			continue
		}
		for _, name := range info.Env(u.Service, u.TLS.Plaintext()) {
			if _, taken := owner[name]; taken {
				// A second service of a type: compose prefixes its names.
				name = envPrefixed(u.Service, name)
			}
			owner[name] = fmt.Sprintf("%s (%s)", join(p, fmt.Sprintf("uses[%d]", i)), u.Service)
		}
	}
	set := func(name, path string) {
		if prev, taken := owner[name]; taken {
			add(Violation{Path: path, Message: fmt.Sprintf("gives the app %s, which %s already sets; one would overwrite the other (rename the service or app, or drop one declaration)", name, prev)})
			return
		}
		owner[name] = path
	}
	targets := map[string]bool{}
	logDir := false
	for i, sc := range app.Sidecars {
		sp := fmt.Sprintf("%s.sidecars[%d]", p, i)
		switch {
		case sc.Envoy != nil:
			if sc.Envoy.Inbound != nil {
				set("BB_LISTEN_ADDR", join(sp, "inbound"))
			}
			for j, o := range sc.Envoy.Outbound {
				if o.Service != "" && !targets[o.Service] { // a repeated target is reported as such
					targets[o.Service] = true
					set(TargetEnv(o.Service), fmt.Sprintf("%s.outbound[%d].service", sp, j))
				}
			}
		case sc.FluentBit != nil && !logDir:
			// Every Fluent Bit sidecar tails the same directory, so a
			// second one gives the app the same BB_LOG_DIR.
			logDir = true
			set("BB_LOG_DIR", sp)
		}
	}
}

// envPrefixed mirrors secrets.EnvPrefixed, which spec cannot import.
func envPrefixed(service, name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(service)) + "_" + name
}

func validateEnvoy(s *Spec, types TypeLookup, app App, sp string, e *EnvoySidecar, add func(error), pod *portClaims, owner string, targets map[string]string) {
	pod.claim(EnvoyAdminPort, owner+" (admin)", sp)
	if in := e.Inbound; in != nil {
		ip := join(sp, "inbound")
		add(gate.Join(
			check(func(in EnvoyInbound) bool { return isPort(in.Port) }, join(ip, "port"), "must be between 1 and 65535"),
			check(func(in EnvoyInbound) bool { return isPort(in.AppPort) }, join(ip, "app_port"), "must be between 1 and 65535"),
			check(func(in EnvoyInbound) bool { return in.Port != in.AppPort }, join(ip, "app_port"), "must differ from inbound.port: Envoy listens on port, the app on app_port"),
		)(*in))
		pod.claim(in.Port, owner+" (inbound.port)", join(ip, "port"))
		// inbound.app_port is the app's: claimed with its other ports first.
	}
	for j, o := range e.Outbound {
		op := fmt.Sprintf("%s.outbound[%d]", sp, j)
		add(check(isPort, join(op, "port"), "must be between 1 and 65535")(o.Port))
		pod.claim(o.Port, fmt.Sprintf("%s (outbound to %s)", owner, o.Service), join(op, "port"))
		add(check(func(l string) bool { return slices.Contains(envoyLBPolicies, l) }, join(op, "lb"), "must be one of %s", strings.Join(envoyLBPolicies, ", "))(o.LB))
		add(check(positiveDuration, join(op, "timeout"), "must be a positive duration such as 5s")(o.Timeout))
		add(check(func(r *int) bool { return *r >= 0 && *r <= 10 }, join(op, "retries"), "must be between 0 and 10")(o.Retries))
		if l := o.Outlier; l != nil {
			lp := join(op, "outlier")
			add(check(func(n int) bool { return n >= 1 }, join(lp, "consecutive_5xx"), "must be at least 1")(l.Consecutive5xx))
			add(check(positiveDuration, join(lp, "ejection"), "must be a positive duration such as 30s")(l.Ejection))
			add(check(func(n int) bool { return n >= 1 && n <= 100 }, join(lp, "max_ejection_percent"), "must be between 1 and 100")(l.MaxEjectionPercent))
			add(check(positiveDuration, join(lp, "interval"), "must be a positive duration such as 10s")(l.Interval))
		}
		if o.Service == "" {
			continue
		}
		if prev, dup := targets[o.Service]; dup {
			add(Violation{Path: join(op, "service"), Message: fmt.Sprintf("%s already has an outbound listener at %s", o.Service, prev)})
			continue
		}
		targets[o.Service] = op
		tp := join(op, "service")
		if o.Service == app.Name {
			add(Violation{Path: tp, Message: "must not be the app itself"})
			continue
		}
		if svc, ok := s.Service(o.Service); ok {
			if info, ok := types(svc.Type); ok && !info.HTTPS {
				add(Violation{Path: tp, Message: fmt.Sprintf("%s (%s) has no TLS HTTP listener", o.Service, svc.Type)})
			}
			continue
		}
		target, ok := s.App(o.Service)
		if !ok {
			add(Violation{Path: tp, Message: "must name a service or app"})
			continue
		}
		if _, ok := target.TLSPort(); !ok {
			add(Violation{Path: tp, Message: fmt.Sprintf("app %s serves no TLS port: give it an envoy sidecar with inbound, or register with the port it serves TLS on", o.Service)})
		}
	}
	if t := e.Transparent; t != nil {
		tp := join(sp, "transparent")
		pod.claim(EnvoyOutboundPort, owner+" (transparent outbound)", tp)
		pod.claim(EnvoyInboundPort, owner+" (transparent inbound)", tp)
		egress := t.Egress
		add(check(func(e string) bool { return slices.Contains(egressPolicies, e) }, join(tp, "egress"), "must be allow or deny")(egress))
	}
	if e.File != "" {
		fp := join(sp, "file")
		add(gate.Join(
			check(isRelativePath, fp, "must be a path relative to experiment.yaml"),
			check(isUsablePath, fp, "must not contain a colon, comma, or newline, and must not point into .bb"),
		)(e.File))
	}
}

// usedService checks that a sidecar's service is in the app's uses, has the
// wanted catalog type, and is reached over TLS.
func usedService(s *Spec, app App, path, name, typ, what string, add func(error)) {
	i := slices.IndexFunc(app.Uses, func(u Use) bool { return u.Service == name })
	if i < 0 {
		add(Violation{Path: path, Message: fmt.Sprintf("must be %s in this app's uses", what)})
		return
	}
	if svc, ok := s.Service(name); ok && svc.Type != typ {
		add(Violation{Path: path, Message: fmt.Sprintf("must be %s, not %s", what, svc.Type)})
	}
	if app.Uses[i].TLS.Plaintext() {
		add(Violation{Path: path, Message: fmt.Sprintf("the sidecar connects to %s over TLS; remove tls.mode: off from its uses entry", name)})
	}
}

// appOwner owns every port the app itself listens on. Its declarations
// may name one listener more than once (health.port equal to
// inbound.app_port), so they share one owner and never collide.
const appOwner = "the app"

// claimAppPorts claims the ports the app declares it listens on, before
// any sidecar port, so a sidecar that would take one is the one reported:
// health.port (with its default), register.port, and every Envoy
// inbound.app_port. A register.port equal to an Envoy inbound.port names
// Envoy's TLS listener (the registry checks it), not one of the app's.
func claimAppPorts(app App, p string, pod *portClaims) {
	inbound := map[int]bool{}
	for i, sc := range app.Sidecars {
		if sc.Envoy != nil && sc.Envoy.Inbound != nil {
			in := sc.Envoy.Inbound
			inbound[in.Port] = true
			if in.AppPort != in.Port { // equal ports are reported by the inbound checks
				pod.claim(in.AppPort, appOwner, fmt.Sprintf("%s.sidecars[%d].inbound.app_port", p, i))
			}
		}
	}
	if h := app.Health; h != nil {
		port, path := h.Port, join(p, "health.port")
		if port == 0 {
			port, path = DefaultHealthPort, join(p, "health")
		}
		pod.claim(port, appOwner, path)
	}
	if r := app.Register; r != nil && !inbound[r.Port] {
		pod.claim(r.Port, appOwner, join(p, "register.port"))
	}
}

// portClaims finds ports bound twice inside one pod.
type portClaims struct {
	owners map[int]string
	paths  map[int]string // where each port was first declared
	add    func(error)
}

func newPortClaims(add func(error)) *portClaims {
	return &portClaims{owners: map[int]string{}, paths: map[int]string{}, add: add}
}

func (c *portClaims) claim(port int, owner, path string) {
	if !isPort(port) {
		return // reported by the field's own check
	}
	if prev, ok := c.owners[port]; ok {
		if prev != owner {
			c.add(Violation{Path: path, Message: fmt.Sprintf("port %d is already used in this pod by %s, declared at %s", port, prev, c.paths[port])})
		}
		return
	}
	c.owners[port] = owner
	c.paths[port] = path
}
