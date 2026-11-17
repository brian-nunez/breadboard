package sidecar

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
)

// Users transparent mode's rules depend on.
const (
	// EnvoyUID is the envoy user of envoyproxy/envoy; its entrypoint drops
	// to it outside transparent pods.
	EnvoyUID = 101
	// TransparentEnvoyUID is the reserved user Envoy runs as in a
	// transparent pod (the image's entrypoint renumbers its envoy user to
	// ENVOY_UID). The rules never redirect this user's traffic, so no app
	// process may run as it: the app keeps no capability to change users.
	TransparentEnvoyUID = 1337
	// TransparentAgentUID is the reserved user a registration agent runs
	// as in a transparent pod (CONSUL_UID of hashicorp/consul's
	// entrypoint), so its gossip and RPC are not redirected either.
	TransparentAgentUID = 1338
	// consulGossipPort is the agent's Serf LAN port, which the registry
	// server dials into the pod.
	consulGossipPort = 8301
	// EnvoyConfigFile is where an Envoy sidecar reads its configuration:
	// the generated one, or the user's file.
	EnvoyConfigFile = catalog.ConfigDir + "/envoy.yaml"
)

// Envoy listener and cluster names, read by explorers and tests.
const (
	envoyInboundListener     = "inbound"
	envoyAppCluster          = "app"
	envoyTransparentOut      = "transparent_outbound"
	envoyTransparentIn       = "transparent_inbound"
	envoyPassthroughCluster  = "passthrough"
	envoyInboundPassCluster  = "inbound_passthrough"
	envoyOutboundPrefix      = "outbound_"
	envoyUpstreamConnTimeout = "2s"
)

// Type URLs of the Envoy extensions the generated config uses.
const (
	typeHCM         = "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"
	typeRouter      = "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"
	typeTCPProxy    = "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy"
	typeRBAC        = "type.googleapis.com/envoy.extensions.filters.network.rbac.v3.RBAC"
	typeSetState    = "type.googleapis.com/envoy.extensions.filters.network.set_filter_state.v3.Config"
	typeOriginalDst = "type.googleapis.com/envoy.extensions.filters.listener.original_dst.v3.OriginalDst"
	typeTLSInspect  = "type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector"
	typeDownTLS     = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext"
	typeUpTLS       = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext"
	typeStdoutLog   = "type.googleapis.com/envoy.extensions.access_loggers.stream.v3.StdoutAccessLog"
)

func renderEnvoy(in Input) (Output, error) {
	e := in.Spec.Envoy
	if e == nil {
		return Output{}, fmt.Errorf("sidecar %s: no envoy settings", in.Name)
	}
	cfg := in.UserFile
	if e.File == "" {
		var err error
		if cfg, err = envoyConfig(in); err != nil {
			return Output{}, fmt.Errorf("sidecar %s: %w", in.Name, err)
		}
	}
	out := Output{Container: catalog.Container{
		// The image's entrypoint runs Envoy as its envoy user: EnvoyUID,
		// or TransparentEnvoyUID in a transparent pod.
		Command: []string{"envoy", "-c", EnvoyConfigFile, "--service-node", in.Name, "--service-cluster", in.App.Name},
		Files:   []catalog.File{{Name: "envoy.yaml", Data: cfg}},
		Health: catalog.Health{
			// The image has bash but no curl: ask the admin API whether
			// Envoy finished initializing (GET /ready answers 200 then).
			Test:          []string{"CMD", "bash", "-c", httpCheck(spec.EnvoyAdminPort, "/ready")},
			Interval:      10 * time.Second,
			Timeout:       3 * time.Second,
			Retries:       3,
			StartPeriod:   30 * time.Second,
			StartInterval: 1 * time.Second,
		},
	}}
	if in := e.Inbound; in != nil {
		out.AppEnv = append(out.AppEnv, catalog.EnvVar{Name: "BB_LISTEN_ADDR", Value: "127.0.0.1:" + strconv.Itoa(in.AppPort)})
	}
	for _, t := range in.Targets {
		out.AppEnv = append(out.AppEnv, catalog.EnvVar{Name: TargetEnv(t.Service), Value: fmt.Sprintf("http://127.0.0.1:%d", t.Outbound.Port)})
		if !t.App && !slices.Contains(out.Needs, t.Service) {
			out.Needs = append(out.Needs, t.Service)
		}
	}
	if e.Transparent != nil {
		out.Environment = append(out.Environment,
			catalog.EnvVar{Name: "ENVOY_UID", Value: strconv.Itoa(TransparentEnvoyUID)},
			catalog.EnvVar{Name: "ENVOY_GID", Value: strconv.Itoa(TransparentEnvoyUID)},
		)
		out.Init = &Init{
			Dockerfile: NetinitDockerfile(),
			Command:    []string{"/bin/sh", "-c", netinitScript(in)},
			CapAdd:     []string{"NET_ADMIN"},
			Resources:  NetinitResources,
		}
	}
	return out, nil
}

// TargetEnv is the variable an app receives for an Envoy outbound target:
// API_URL for api, ORDER_SERVICE_URL for order-service.
func TargetEnv(service string) string { return spec.TargetEnv(service) }

// httpCheck is a bash script that sends GET path to 127.0.0.1:port and
// succeeds on a 200 status line (Fluent Bit sends no reason phrase). It contains no dollar signs, so compose
// interpolation leaves it alone.
func httpCheck(port int, path string) string {
	return fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/%d && printf 'GET %s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n' >&3 && head -n 1 <&3 | grep -q ' 200'`, port, path)
}

// NetinitDockerfile builds transparent mode's init image: the pod image
// plus iptables, installed at build time.
func NetinitDockerfile() []byte {
	return []byte("# Generated by Breadboard: transparent mode's init container.\n" +
		"FROM " + PodImage + "\n" +
		"RUN apk add --no-cache iptables\n")
}

// netinitScript installs the pod's traffic rules. It is idempotent (the
// container may run again in an existing pod) and prints what it installed.
//
// Outbound: every TCP connection the pod opens goes to Envoy's 15001,
// except Envoy's own (TransparentEnvoyUID), a registration agent's
// (TransparentAgentUID), and loopback.
//
// Inbound: only declared external listeners accept connections into the
// pod: Envoy's TLS inbound port (reached directly, so its handshake is
// Envoy's own), the app's own TLS ports (redirected to Envoy's 15006, which
// hands the still-encrypted bytes to the app on loopback), and the agent's
// gossip port. Every other TCP connection into the pod is reset, so the
// app's plaintext port and every loopback listener (Envoy's admin, outbound
// listeners, PgBouncer, Fluent Bit) stay unreachable from outside.
func netinitScript(in Input) string {
	agent := slices.Contains(in.Siblings, spec.SidecarConsulAgent)
	exemptUIDs := []int{TransparentEnvoyUID}
	var accept []int // external listeners reached directly
	if agent {
		exemptUIDs = append(exemptUIDs, TransparentAgentUID)
		accept = append(accept, consulGossipPort)
	}
	if i := in.Spec.Envoy.Inbound; i != nil {
		accept = append(accept, i.Port)
	}
	lines := []string{
		"set -eu",
		"# Generated by Breadboard: transparent mode for " + in.App.Service + ".",
		"chain() { iptables -t \"$$1\" -N \"$$2\" 2>/dev/null || iptables -t \"$$1\" -F \"$$2\"; }",
		"hook() { iptables -t \"$$1\" -C \"$$2\" -p tcp -j \"$$3\" 2>/dev/null || iptables -t \"$$1\" -A \"$$2\" -p tcp -j \"$$3\"; }",
		"# Outbound: the pod's connections go to Envoy on " + strconv.Itoa(spec.EnvoyOutboundPort) + ".",
		"chain nat BB_OUT",
	}
	for _, uid := range exemptUIDs {
		lines = append(lines, fmt.Sprintf("iptables -t nat -A BB_OUT -m owner --uid-owner %d -j RETURN", uid))
	}
	lines = append(lines,
		"iptables -t nat -A BB_OUT -d 127.0.0.0/8 -j RETURN",
		fmt.Sprintf("iptables -t nat -A BB_OUT -p tcp -j REDIRECT --to-ports %d", spec.EnvoyOutboundPort),
		"hook nat OUTPUT BB_OUT",
		"# Inbound: the app's own TLS ports go to Envoy on "+strconv.Itoa(spec.EnvoyInboundPort)+".",
		"chain nat BB_IN",
	)
	for _, p := range in.AppTLSPorts {
		lines = append(lines, fmt.Sprintf("iptables -t nat -A BB_IN -p tcp --dport %d -j REDIRECT --to-ports %d", p, spec.EnvoyInboundPort))
	}
	lines = append(lines,
		"hook nat PREROUTING BB_IN",
		"# Inbound: only declared external listeners accept connections; the rest are reset.",
		"chain filter BB_IN",
		"iptables -t filter -A BB_IN -i lo -j RETURN",
		"iptables -t filter -A BB_IN -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN",
	)
	for _, p := range accept {
		lines = append(lines, fmt.Sprintf("iptables -t filter -A BB_IN -p tcp --dport %d -j RETURN", p))
	}
	if len(in.AppTLSPorts) > 0 {
		lines = append(lines, fmt.Sprintf("iptables -t filter -A BB_IN -p tcp --dport %d -j RETURN", spec.EnvoyInboundPort))
	}
	lines = append(lines,
		"iptables -t filter -A BB_IN -p tcp -j REJECT --reject-with tcp-reset",
		"hook filter INPUT BB_IN",
		"echo 'breadboard: transparent mode rules installed:'",
		"iptables -t nat -S",
		"iptables -t filter -S",
	)
	return strings.Join(lines, "\n")
}

// envoyConfig generates the sidecar's static Envoy configuration.
func envoyConfig(in Input) ([]byte, error) {
	e := in.Spec.Envoy
	var listeners, clusters list
	if i := e.Inbound; i != nil {
		listeners = append(listeners, o(
			"name", envoyInboundListener,
			"address", socket("0.0.0.0", i.Port),
			"traffic_direction", "INBOUND",
			"filter_chains", list{o(
				"filters", list{hcm("inbound", routeConfig("inbound", o(
					"name", "app",
					"domains", list{"*"},
					"routes", list{o("match", o("prefix", "/"), "route", o("cluster", envoyAppCluster))},
					// This listener only accepts TLS, so the app learns the
					// request reached its pod encrypted, whatever a client
					// sent in the header.
					"request_headers_to_add", list{o(
						"header", o("key", "x-forwarded-proto", "value", "https"),
						"append_action", "OVERWRITE_IF_EXISTS_OR_ADD",
					)},
				)))},
				"transport_socket", o(
					"name", "envoy.transport_sockets.tls",
					"typed_config", o(
						"@type", typeDownTLS,
						"common_tls_context", o(
							"alpn_protocols", list{"h2", "http/1.1"},
							"tls_certificates", list{o(
								"certificate_chain", o("filename", catalog.CertFile),
								"private_key", o("filename", catalog.KeyFile),
							)},
						),
					),
				),
			)},
		))
		clusters = append(clusters, o(
			"name", envoyAppCluster,
			"type", "STATIC",
			"connect_timeout", "1s",
			"load_assignment", endpoints(envoyAppCluster, "127.0.0.1", i.AppPort),
		))
	}
	for _, t := range in.Targets {
		route, err := targetRoute(t)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, o(
			"name", envoyOutboundPrefix+t.Service,
			"address", socket("127.0.0.1", t.Outbound.Port),
			"traffic_direction", "OUTBOUND",
			"filter_chains", list{o("filters", list{hcm(envoyOutboundPrefix+t.Service, routeConfig(envoyOutboundPrefix+t.Service, o(
				"name", t.Service,
				"domains", list{"*"},
				"routes", list{route},
			)))})},
		))
		c, err := targetCluster(t)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	if e.Transparent != nil {
		ls, cs, err := transparent(in)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, ls...)
		clusters = append(clusters, cs...)
	}
	doc := o(
		"node", o("id", in.Name, "cluster", in.App.Name),
		"admin", o("address", socket("127.0.0.1", spec.EnvoyAdminPort)),
		"static_resources", o("listeners", listeners, "clusters", clusters),
	)
	return encodeYAML(fmt.Sprintf("Generated by Breadboard for %s, the Envoy sidecar of %s.\n"+
		"To own this file, copy it next to experiment.yaml and set file: on the sidecar.", in.Name, in.App.Service), doc)
}

// transparent returns the listeners and clusters of transparent mode.
//
// 15001 receives every outbound TCP connection the rules redirect.
//
//   - egress allow: plain HTTP to a declared target's TLS port is routed by
//     Host to the target (TLS origination), another host passes through at
//     the HTTP level; every other connection passes through untouched.
//   - egress deny: only declared destinations are reachable, matched by
//     port and address (every container has a stable address in a lab
//     with a transparent pod). A declared app or infra target's address
//     and TLS port accept plain HTTP (routed by Host; 403 for another
//     host) and the app's own TLS (passed through); each service in uses
//     is reachable on its address and port; everything else is closed.
//
// 15006 receives the inbound connections the rules redirect, which are only
// those to the app's own TLS ports, and forwards each to the same port on
// 127.0.0.1. Any other connection that reaches it is closed.
func transparent(in Input) (list, list, error) {
	deny := in.Spec.Envoy.Transparent.Egress == spec.EgressDeny
	var ports []int // declared targets' ports, in order
	byPort := map[int][]Target{}
	for _, t := range in.Targets {
		if _, ok := byPort[t.Port]; !ok {
			ports = append(ports, t.Port)
		}
		byPort[t.Port] = append(byPort[t.Port], t)
	}
	var chains list
	for _, p := range ports {
		var hosts list
		var addrs []string
		for _, t := range byPort[p] {
			route, err := targetRoute(t)
			if err != nil {
				return nil, nil, err
			}
			hosts = append(hosts, o(
				"name", t.Service,
				"domains", list{t.FQDN, fmt.Sprintf("%s:%d", t.FQDN, p)},
				"routes", list{route},
			))
			addrs = append(addrs, t.Addresses...)
		}
		other := o("match", o("prefix", "/"), "route", o("cluster", envoyPassthroughCluster))
		if deny {
			other = o("match", o("prefix", "/"), "direct_response", o(
				"status", 403,
				"body", o("inline_string", "breadboard: egress denied: this host is not a declared outbound target\n"),
			))
		}
		hosts = append(hosts, o("name", "undeclared", "domains", list{"*"}, "routes", list{other}))
		if deny && len(addrs) == 0 {
			continue // no known address: nothing on this port is declared reachable
		}
		match := func(transport string) obj {
			m := o("destination_port", p)
			if deny {
				m = append(m, field{"prefix_ranges", prefixRanges(addrs)})
			}
			return append(m, field{"transport_protocol", transport})
		}
		name := fmt.Sprintf("transparent_http_%d", p)
		chains = append(chains, o(
			"name", name,
			"filter_chain_match", match("raw_buffer"),
			"filters", list{hcm(name, routeConfig(name, hosts...))},
		))
		// TLS the app originates itself to a target is passed through
		// untouched: the app verifies the target's certificate.
		chains = append(chains, o(
			"name", fmt.Sprintf("transparent_tls_%d", p),
			"filter_chain_match", match("tls"),
			"filters", list{tcpProxy(fmt.Sprintf("passthrough_%d", p), envoyPassthroughCluster)},
		))
	}
	if deny {
		var usesPorts []int
		usesAddrs := map[int][]string{}
		for _, u := range in.Uses {
			if u.Port == 0 || len(u.Addresses) == 0 {
				continue
			}
			if _, ok := usesAddrs[u.Port]; !ok {
				usesPorts = append(usesPorts, u.Port)
			}
			usesAddrs[u.Port] = append(usesAddrs[u.Port], u.Addresses...)
		}
		for _, p := range usesPorts {
			chains = append(chains, o(
				"name", fmt.Sprintf("uses_%d", p),
				"filter_chain_match", o("destination_port", p, "prefix_ranges", prefixRanges(usesAddrs[p])),
				"filters", list{tcpProxy(fmt.Sprintf("passthrough_%d", p), envoyPassthroughCluster)},
			))
		}
	}
	fallback := o("name", "passthrough", "filters", list{tcpProxy("passthrough", envoyPassthroughCluster)})
	if deny {
		fallback = denyChain("egress_denied")
	}
	filters := list{o("name", "envoy.filters.listener.original_dst", "typed_config", o("@type", typeOriginalDst))}
	if len(ports) > 0 {
		// Telling plain HTTP from TLS needs the first bytes; only
		// connections to a declared target's port wait for them, so
		// server-first protocols elsewhere are untouched.
		inspector := o("name", "envoy.filters.listener.tls_inspector", "typed_config", o("@type", typeTLSInspect))
		var rules list
		for _, p := range ports {
			rules = append(rules, o("destination_port_range", o("start", p, "end", p+1)))
		}
		match := rules[0]
		if len(rules) > 1 {
			match = o("or_match", o("rules", rules))
		}
		inspector = append(inspector, field{"filter_disabled", o("not_match", match)})
		filters = append(filters, inspector)
	}
	out := o(
		"name", envoyTransparentOut,
		"address", socket("0.0.0.0", spec.EnvoyOutboundPort),
		"traffic_direction", "OUTBOUND",
		"listener_filters", filters,
	)
	if len(ports) > 0 {
		out = append(out, field{"continue_on_listener_filters_timeout", true})
	}
	if len(chains) > 0 {
		out = append(out, field{"filter_chains", chains})
	}
	out = append(out, field{"default_filter_chain", fallback})

	var inChains list
	for _, p := range in.AppTLSPorts {
		inChains = append(inChains, o(
			"name", fmt.Sprintf("app_tls_%d", p),
			"filter_chain_match", o("destination_port", p),
			"filters", list{
				// The original destination is the pod's address; the app may
				// listen on loopback only, so connect to 127.0.0.1 on the
				// original port instead. The bytes stay the client's TLS.
				o("name", "envoy.filters.network.set_filter_state", "typed_config", o(
					"@type", typeSetState,
					"on_new_connection", list{o(
						"object_key", "envoy.network.transport_socket.original_dst_address",
						"format_string", o("text_format_source", o("inline_string", "127.0.0.1:%DOWNSTREAM_LOCAL_PORT%")),
					)},
				)),
				tcpProxy("inbound_passthrough", envoyInboundPassCluster),
			},
		))
	}
	inbound := o(
		"name", envoyTransparentIn,
		"address", socket("0.0.0.0", spec.EnvoyInboundPort),
		"traffic_direction", "INBOUND",
		"listener_filters", list{o("name", "envoy.filters.listener.original_dst", "typed_config", o("@type", typeOriginalDst))},
	)
	if len(inChains) > 0 {
		inbound = append(inbound, field{"filter_chains", inChains})
	}
	inbound = append(inbound, field{"default_filter_chain", denyChain("ingress_denied")})
	clusters := list{
		o("name", envoyPassthroughCluster, "type", "ORIGINAL_DST", "lb_policy", "CLUSTER_PROVIDED", "connect_timeout", envoyUpstreamConnTimeout),
		o("name", envoyInboundPassCluster, "type", "ORIGINAL_DST", "lb_policy", "CLUSTER_PROVIDED", "connect_timeout", "1s"),
	}
	return list{out, inbound}, clusters, nil
}

// denyChain closes every connection it receives, counting it in
// <statPrefix>.rbac.denied.
func denyChain(statPrefix string) obj {
	return o("name", statPrefix, "filters", list{
		o("name", "envoy.filters.network.rbac", "typed_config", o(
			"@type", typeRBAC,
			"stat_prefix", statPrefix,
			"rules", o("action", "DENY", "policies", o("undeclared", o(
				"permissions", list{o("any", true)},
				"principals", list{o("any", true)},
			))),
		)),
		tcpProxy(statPrefix, envoyPassthroughCluster),
	})
}

// prefixRanges matches each address exactly, in order, without repeats.
func prefixRanges(addrs []string) list {
	var out list
	seen := map[string]bool{}
	for _, a := range addrs {
		if !seen[a] {
			seen[a] = true
			out = append(out, o("address_prefix", a, "prefix_len", 32))
		}
	}
	return out
}

// targetRoute routes every request to a target's cluster with its
// timeout, retries and hashing.
func targetRoute(t Target) (obj, error) {
	ob := t.Outbound
	timeout, err := protoDuration(ob.Timeout)
	if err != nil {
		return nil, fmt.Errorf("outbound %s: timeout: %w", t.Service, err)
	}
	route := o("cluster", envoyOutboundPrefix+t.Service, "timeout", timeout)
	if ob.Retries != nil && *ob.Retries > 0 {
		route = append(route, field{"retry_policy", o(
			"retry_on", "connect-failure,refused-stream,reset,5xx",
			"num_retries", *ob.Retries,
		)})
	}
	if ob.LB == "ring_hash" || ob.LB == "maglev" {
		// The app is the only client of its sidecar, so hash on the
		// connection's source: each app instance sticks to one upstream.
		route = append(route, field{"hash_policy", list{o("connection_properties", o("source_ip", true))}})
	}
	return o("match", o("prefix", "/"), "route", route), nil
}

// targetCluster is a target's upstream: every address of its shared name,
// over TLS verified against the lab CA for that name. Envoy re-resolves
// the name every 5 seconds (its default), so replicas that start later
// join the pool.
func targetCluster(t Target) (obj, error) {
	lb, ok := envoyLBPolicies[t.Outbound.LB]
	if !ok {
		return nil, fmt.Errorf("outbound %s: unknown lb %q", t.Service, t.Outbound.LB)
	}
	name := envoyOutboundPrefix + t.Service
	c := o(
		"name", name,
		"type", "STRICT_DNS",
		"dns_lookup_family", "V4_ONLY",
		"connect_timeout", envoyUpstreamConnTimeout,
		"lb_policy", lb,
		"load_assignment", endpoints(name, t.FQDN, t.Port),
		"transport_socket", o(
			"name", "envoy.transport_sockets.tls",
			"typed_config", o(
				"@type", typeUpTLS,
				"sni", t.FQDN,
				"common_tls_context", o("validation_context", o(
					"trusted_ca", o("filename", catalog.CAFile),
					"match_typed_subject_alt_names", list{o("san_type", "DNS", "matcher", o("exact", t.FQDN))},
				)),
			),
		),
	)
	if l := t.Outbound.Outlier; l != nil {
		ejection, err := protoDuration(l.Ejection)
		if err != nil {
			return nil, fmt.Errorf("outbound %s: outlier ejection: %w", t.Service, err)
		}
		od := o("consecutive_5xx", l.Consecutive5xx, "base_ejection_time", ejection)
		// Envoy never ejects more than this share of the cluster; its own
		// default (10%) ejects no host of three.
		if l.MaxEjectionPercent != 0 {
			od = append(od, field{"max_ejection_percent", l.MaxEjectionPercent})
		}
		if l.Interval != "" {
			interval, err := protoDuration(l.Interval)
			if err != nil {
				return nil, fmt.Errorf("outbound %s: outlier interval: %w", t.Service, err)
			}
			od = append(od, field{"interval", interval})
		}
		c = append(c, field{"outlier_detection", od})
	}
	return c, nil
}

var envoyLBPolicies = map[string]string{
	"round_robin":   "ROUND_ROBIN",
	"least_request": "LEAST_REQUEST",
	"random":        "RANDOM",
	"ring_hash":     "RING_HASH",
	"maglev":        "MAGLEV",
}

func hcm(statPrefix string, routes obj) obj {
	return o("name", "envoy.filters.network.http_connection_manager", "typed_config", o(
		"@type", typeHCM,
		"stat_prefix", statPrefix,
		"codec_type", "AUTO",
		"access_log", list{o("name", "envoy.access_loggers.stdout", "typed_config", o("@type", typeStdoutLog))},
		"route_config", routes,
		"http_filters", list{o("name", "envoy.filters.http.router", "typed_config", o("@type", typeRouter))},
	))
}

func routeConfig(name string, hosts ...any) obj {
	return o("name", name, "virtual_hosts", list(hosts))
}

func tcpProxy(statPrefix, cluster string) obj {
	return o("name", "envoy.filters.network.tcp_proxy", "typed_config", o(
		"@type", typeTCPProxy,
		"stat_prefix", statPrefix,
		"cluster", cluster,
		"access_log", list{o("name", "envoy.access_loggers.stdout", "typed_config", o("@type", typeStdoutLog))},
	))
}

func socket(addr string, port int) obj {
	return o("socket_address", o("address", addr, "port_value", port))
}

func endpoints(cluster, addr string, port int) obj {
	return o("cluster_name", cluster, "endpoints", list{o("lb_endpoints", list{o("endpoint", o("address", socket(addr, port)))})})
}

// protoDuration converts a Go duration to the protobuf JSON form Envoy
// reads ("1.5s"): Go forms such as 1m30s are not valid there.
func protoDuration(s string) (string, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s", nil
}

// PlaintextGuardScript is the netinit script that confines a terminate
// target's plaintext port to its private segment, whatever address the app
// binds: TCP to port is dropped unless it is addressed to addr, the
// instance's segment address. The rules live in the pod's namespace; the
// app holds no NET_ADMIN, so it cannot remove them. The chain is rebuilt on
// every run, so a changed port or address replaces the old rule.
func PlaintextGuardScript(port int, addr string) string {
	return strings.Join([]string{
		"set -eu",
		fmt.Sprintf("# Plaintext guard: TCP to port %d is accepted only on the segment address %s.", port, addr),
		"iptables -N BB_PLAIN 2>/dev/null || iptables -F BB_PLAIN",
		"iptables -C INPUT -j BB_PLAIN 2>/dev/null || iptables -I INPUT 1 -j BB_PLAIN",
		fmt.Sprintf("iptables -A BB_PLAIN -p tcp --dport %d ! -d %s -j DROP", port, addr),
		"iptables -S BB_PLAIN",
	}, "\n")
}

// NetinitResources are the init container's limits.
var NetinitResources = catalog.Resources{Memory: "32m", CPUs: "0.1"}
