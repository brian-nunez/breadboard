package sidecar

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
)

func secretRef(service string) func(string) string {
	return func(key string) string { return "${" + service + "/" + key + "}" }
}

func pgUse() Use {
	return Use{Service: "postgres", Type: "postgres", FQDN: "postgres.bb.internal", Port: 5432, TLS: true,
		Config: map[string]string{"database": "orders", "user": "app"},
		Env: []catalog.EnvVar{
			{Name: "DATABASE_URL", Value: "postgres://app:pw@postgres.bb.internal:5432/orders?sslmode=verify-full"},
			{Name: "PGHOST", Value: "postgres.bb.internal"}, {Name: "PGPORT", Value: "5432"},
			{Name: "PGUSER", Value: "app"}, {Name: "PGSSLMODE", Value: "verify-full"},
		}}
}

// envoyDoc is the part of an Envoy config the tests read.
type envoyDoc struct {
	Admin struct {
		Address struct {
			SocketAddress struct {
				Address string `yaml:"address"`
				Port    int    `yaml:"port_value"`
			} `yaml:"socket_address"`
		} `yaml:"address"`
	} `yaml:"admin"`
	StaticResources struct {
		Listeners []struct {
			Name    string `yaml:"name"`
			Address struct {
				SocketAddress struct {
					Address string `yaml:"address"`
					Port    int    `yaml:"port_value"`
				} `yaml:"socket_address"`
			} `yaml:"address"`
			ListenerFilters []map[string]any `yaml:"listener_filters"`
			FilterChains    []struct {
				Name  string         `yaml:"name"`
				Match map[string]any `yaml:"filter_chain_match"`
			} `yaml:"filter_chains"`
			DefaultChain *struct {
				Name string `yaml:"name"`
			} `yaml:"default_filter_chain"`
		} `yaml:"listeners"`
		Clusters []struct {
			Name     string         `yaml:"name"`
			Type     string         `yaml:"type"`
			LBPolicy string         `yaml:"lb_policy"`
			Outlier  map[string]any `yaml:"outlier_detection"`
			Load     struct {
				Endpoints []struct {
					LB []struct {
						Endpoint struct {
							Address struct {
								SocketAddress struct {
									Address string `yaml:"address"`
									Port    int    `yaml:"port_value"`
								} `yaml:"socket_address"`
							} `yaml:"address"`
						} `yaml:"endpoint"`
					} `yaml:"lb_endpoints"`
				} `yaml:"endpoints"`
			} `yaml:"load_assignment"`
			TransportSocket struct {
				TypedConfig struct {
					SNI    string `yaml:"sni"`
					Common struct {
						Validation struct {
							CA struct {
								Filename string `yaml:"filename"`
							} `yaml:"trusted_ca"`
							SANs []struct {
								Matcher struct {
									Exact string `yaml:"exact"`
								} `yaml:"matcher"`
							} `yaml:"match_typed_subject_alt_names"`
						} `yaml:"validation_context"`
					} `yaml:"common_tls_context"`
				} `yaml:"typed_config"`
			} `yaml:"transport_socket"`
		} `yaml:"clusters"`
	} `yaml:"static_resources"`
}

func renderEnvoyDoc(t *testing.T, in Input) (Output, envoyDoc) {
	t.Helper()
	typ, _ := Lookup(spec.SidecarEnvoy)
	out, err := typ.RenderSidecar(in)
	if err != nil {
		t.Fatal(err)
	}
	var doc envoyDoc
	if err := yaml.Unmarshal(out.Files[0].Data, &doc); err != nil {
		t.Fatalf("generated Envoy config is not YAML: %v\n%s", err, out.Files[0].Data)
	}
	return out, doc
}

func envoyInput(e spec.EnvoySidecar, targets ...Target) Input {
	sc := spec.Sidecar{Type: spec.SidecarEnvoy, Envoy: &e}.WithDefaults()
	for i := range targets {
		targets[i].Outbound = sc.Envoy.Outbound[i]
	}
	return Input{
		Name: "api-envoy", Domain: "bb.internal", Spec: sc, Targets: targets,
		App:    App{Service: "api", Name: "api", FQDN: "api.bb.internal", SharedFQDN: "api.bb.internal"},
		Uses:   []Use{pgUse()},
		Secret: secretRef,
	}
}

func TestEnvoyInboundAndOutbound(t *testing.T) {
	retries := 0
	in := envoyInput(spec.EnvoySidecar{
		Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080},
		Outbound: []spec.EnvoyOutbound{
			{Service: "orders", Port: 9001, LB: "maglev", Timeout: "1m30s", Outlier: &spec.Outlier{Consecutive5xx: 5, Ejection: "30s", MaxEjectionPercent: 50, Interval: "5s"}},
			{Service: "s3", Port: 9002, Retries: &retries},
		},
	},
		Target{Service: "orders", App: true, FQDN: "orders.bb.internal", Port: 8443},
		Target{Service: "s3", FQDN: "s3.bb.internal", Port: 8333},
	)
	out, doc := renderEnvoyDoc(t, in)
	if a := doc.Admin.Address.SocketAddress; a.Address != "127.0.0.1" || a.Port != 15000 {
		t.Errorf("admin = %+v", a)
	}
	listeners := map[string]string{}
	for _, l := range doc.StaticResources.Listeners {
		listeners[l.Name] = l.Address.SocketAddress.Address + ":" + strconv.Itoa(l.Address.SocketAddress.Port)
	}
	want := map[string]string{"inbound": "0.0.0.0:8443", "outbound_orders": "127.0.0.1:9001", "outbound_s3": "127.0.0.1:9002"}
	if len(listeners) != len(want) {
		t.Errorf("listeners = %v", listeners)
	}
	for k, v := range want {
		if listeners[k] != v {
			t.Errorf("listener %s = %q, want %s", k, listeners[k], v)
		}
	}
	clusters := map[string]int{}
	for i, c := range doc.StaticResources.Clusters {
		clusters[c.Name] = i
	}
	app := doc.StaticResources.Clusters[clusters["app"]]
	if ep := app.Load.Endpoints[0].LB[0].Endpoint.Address.SocketAddress; app.Type != "STATIC" || ep.Address != "127.0.0.1" || ep.Port != 8080 {
		t.Errorf("app cluster = %+v", app)
	}
	orders := doc.StaticResources.Clusters[clusters["outbound_orders"]]
	tls := orders.TransportSocket.TypedConfig
	if orders.Type != "STRICT_DNS" || orders.LBPolicy != "MAGLEV" || tls.SNI != "orders.bb.internal" ||
		tls.Common.Validation.CA.Filename != catalog.CAFile || len(tls.Common.Validation.SANs) != 1 || tls.Common.Validation.SANs[0].Matcher.Exact != "orders.bb.internal" {
		t.Errorf("orders cluster = %+v", orders)
	}
	if ep := orders.Load.Endpoints[0].LB[0].Endpoint.Address.SocketAddress; ep.Address != "orders.bb.internal" || ep.Port != 8443 {
		t.Errorf("orders endpoint = %+v", ep)
	}
	if orders.Outlier["consecutive_5xx"] != 5 || orders.Outlier["base_ejection_time"] != "30s" ||
		orders.Outlier["max_ejection_percent"] != 50 || orders.Outlier["interval"] != "5s" {
		t.Errorf("outlier = %v", orders.Outlier)
	}
	cfg := string(out.Files[0].Data)
	for _, s := range []string{"timeout: 90s", "num_retries: 2", "source_ip: true", "key: x-forwarded-proto"} {
		if !strings.Contains(cfg, s) {
			t.Errorf("config lacks %q", s)
		}
	}
	if strings.Count(cfg, "num_retries") != 1 {
		t.Error("retries: 0 still rendered a retry policy")
	}
	env := map[string]string{}
	for _, v := range out.AppEnv {
		env[v.Name] = v.Value
	}
	if env["BB_LISTEN_ADDR"] != "127.0.0.1:8080" || env["ORDERS_URL"] != "http://127.0.0.1:9001" || env["S3_URL"] != "http://127.0.0.1:9002" || len(env) != 3 {
		t.Errorf("app env = %v", env)
	}
	if !slices.Equal(out.Needs, []string{"s3"}) || out.Init != nil {
		t.Errorf("needs = %v, init = %v", out.Needs, out.Init)
	}
	if !slices.Equal(out.Command[:3], []string{"envoy", "-c", EnvoyConfigFile}) {
		t.Errorf("command = %v", out.Command)
	}
}

// transparentInput is a deny or allow pod with an inbound listener, a
// declared app target with two replicas, Postgres in uses, a register
// port and a registration agent.
func transparentInput(egress string) Input {
	in := envoyInput(spec.EnvoySidecar{
		Inbound:     &spec.EnvoyInbound{Port: 8443, AppPort: 8080},
		Outbound:    []spec.EnvoyOutbound{{Service: "orders", Port: 9001}},
		Transparent: &spec.Transparent{Egress: egress},
	}, Target{Service: "orders", App: true, FQDN: "orders.bb.internal", Port: 8443, Addresses: []string{"10.213.0.12", "10.213.0.13"}})
	in.Uses[0].Addresses = []string{"10.213.0.10"}
	in.Siblings = []string{spec.SidecarConsulAgent}
	in.Transparent = true
	in.AppTLSPorts = []int{9443}
	return in
}

// chainsOf returns a listener's filter chains as name → match, and its
// default chain's name.
func chainsOf(doc envoyDoc, listener string) (map[string]map[string]any, string) {
	out := map[string]map[string]any{}
	for _, l := range doc.StaticResources.Listeners {
		if l.Name != listener {
			continue
		}
		for _, c := range l.FilterChains {
			out[c.Name] = c.Match
		}
		if l.DefaultChain != nil {
			return out, l.DefaultChain.Name
		}
	}
	return out, ""
}

func prefixes(m map[string]any) []string {
	var out []string
	if pr, ok := m["prefix_ranges"].([]any); ok {
		for _, p := range pr {
			out = append(out, p.(map[string]any)["address_prefix"].(string))
		}
	}
	return out
}

func TestEnvoyTransparentDeny(t *testing.T) {
	out, doc := renderEnvoyDoc(t, transparentInput(spec.EgressDeny))
	chains, fallback := chainsOf(doc, "transparent_outbound")
	if fallback != "egress_denied" || len(chains) != 3 {
		t.Fatalf("chains = %v, default = %s", chains, fallback)
	}
	// The declared app target: plain HTTP (routed by Host) and the app's
	// own TLS, on its port and only at its replicas' addresses.
	for name, transport := range map[string]string{"transparent_http_8443": "raw_buffer", "transparent_tls_8443": "tls"} {
		m := chains[name]
		if m["destination_port"] != 8443 || m["transport_protocol"] != transport || !slices.Equal(prefixes(m), []string{"10.213.0.12", "10.213.0.13"}) {
			t.Errorf("%s match = %v", name, m)
		}
	}
	// Postgres in uses: its port at its address only.
	if m := chains["uses_5432"]; m["destination_port"] != 5432 || !slices.Equal(prefixes(m), []string{"10.213.0.10"}) {
		t.Errorf("uses_5432 match = %v", m)
	}
	cfg := string(out.Files[0].Data)
	if !strings.Contains(cfg, "status: 403") {
		t.Error("deny does not answer undeclared hosts with 403")
	}
	// Inbound: only the app's own TLS port is forwarded to loopback.
	in, inFallback := chainsOf(doc, "transparent_inbound")
	if len(in) != 1 || in["app_tls_9443"]["destination_port"] != 9443 || inFallback != "ingress_denied" {
		t.Errorf("inbound chains = %v, default = %s", in, inFallback)
	}
	if !strings.Contains(cfg, "127.0.0.1:%DOWNSTREAM_LOCAL_PORT%") {
		t.Error("the app's TLS port is not forwarded to loopback")
	}
	env := map[string]string{}
	for _, v := range out.Environment {
		env[v.Name] = v.Value
	}
	if env["ENVOY_UID"] != "1337" || env["ENVOY_GID"] != "1337" {
		t.Errorf("envoy env = %v", env)
	}
	if out.Init == nil || !slices.Equal(out.Init.CapAdd, []string{"NET_ADMIN"}) {
		t.Fatalf("init = %+v", out.Init)
	}
	script := out.Init.Command[2]
	for _, s := range []string{
		"--uid-owner 1337 -j RETURN", "--uid-owner 1338 -j RETURN", "-d 127.0.0.0/8 -j RETURN", "--to-ports 15001",
		"-A BB_IN -p tcp --dport 9443 -j REDIRECT --to-ports 15006",
		"-A BB_IN -i lo -j RETURN", "--ctstate ESTABLISHED,RELATED -j RETURN",
		"-A BB_IN -p tcp --dport 8301 -j RETURN", "-A BB_IN -p tcp --dport 8443 -j RETURN", "-A BB_IN -p tcp --dport 15006 -j RETURN",
		"-A BB_IN -p tcp -j REJECT --reject-with tcp-reset", "hook filter INPUT BB_IN", "iptables -t nat -S", "iptables -t filter -S",
	} {
		if !strings.Contains(script, s) {
			t.Errorf("script lacks %q:\n%s", s, script)
		}
	}
	// Nothing else is redirected or accepted from outside: not the app's
	// plaintext port, the admin API, outbound listeners or other sidecars.
	for _, p := range []string{"8080", "15000", "9001", "6432", "2020"} {
		if strings.Contains(script, "--dport "+p+" ") {
			t.Errorf("port %s is reachable from outside the pod:\n%s", p, script)
		}
	}
	if strings.Contains(script, "--uid-owner 101") || strings.Contains(script, "--uid-owner 100 ") {
		t.Errorf("an image's default user is exempt:\n%s", script)
	}
	if strings.Contains(strings.ReplaceAll(script, "$$", ""), "$") {
		t.Errorf("script has a dollar sign compose would interpolate:\n%s", script)
	}
}

// Without known addresses, deny allows nothing rather than a whole port.
func TestEnvoyTransparentDenyFailsClosed(t *testing.T) {
	in := transparentInput(spec.EgressDeny)
	in.Targets[0].Addresses, in.Uses[0].Addresses = nil, nil
	_, doc := renderEnvoyDoc(t, in)
	if chains, fallback := chainsOf(doc, "transparent_outbound"); len(chains) != 0 || fallback != "egress_denied" {
		t.Errorf("chains = %v, default = %s", chains, fallback)
	}
}

func TestEnvoyTransparentAllow(t *testing.T) {
	out, doc := renderEnvoyDoc(t, transparentInput(spec.EgressAllow))
	chains, fallback := chainsOf(doc, "transparent_outbound")
	if fallback != "passthrough" || len(chains) != 2 || chains["transparent_http_8443"]["prefix_ranges"] != nil || chains["transparent_tls_8443"]["transport_protocol"] != "tls" {
		t.Errorf("chains = %v, default = %s", chains, fallback)
	}
	if strings.Contains(string(out.Files[0].Data), "status: 403") {
		t.Error("allow answers undeclared hosts with 403")
	}
	var ports []int
	for _, l := range doc.StaticResources.Listeners {
		ports = append(ports, l.Address.SocketAddress.Port)
	}
	if !slices.Equal(ports, []int{8443, 9001, 15001, 15006}) {
		t.Errorf("listener ports = %v", ports)
	}
}

// Without a registration agent or app TLS port, only Envoy, loopback and
// the inbound listener are exempt.
func TestNetinitExemptsOnlyWhatThePodRuns(t *testing.T) {
	in := envoyInput(spec.EnvoySidecar{Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080}, Transparent: &spec.Transparent{}})
	script := netinitScript(in)
	if strings.Contains(script, "--uid-owner 1338") || strings.Contains(script, "8301") || strings.Contains(script, "--dport 15006") ||
		!strings.Contains(script, "-A BB_IN -p tcp --dport 8443 -j RETURN") {
		t.Errorf("script:\n%s", script)
	}
}

// A registration agent in a transparent pod runs as the reserved user.
func TestConsulAgentInATransparentPod(t *testing.T) {
	consul, _ := catalog.Lookup("consul")
	typ, _ := Lookup(spec.SidecarConsulAgent)
	in := Input{
		Name: "api-agent", App: App{Service: "api", Name: "api", FQDN: "api.bb.internal"},
		Secret:   func(string) func(string) string { return func(string) string { return "S" } },
		Register: &Register{Entry: consul, RegistryService: "consul", RegistryFQDN: "consul.bb.internal", Service: "api", Port: 8443},
	}
	plain, _ := typ.RenderSidecar(in)
	in.Transparent = true
	tp, _ := typ.RenderSidecar(in)
	has := func(out Output, name string) bool {
		return slices.ContainsFunc(out.Environment, func(v catalog.EnvVar) bool { return v.Name == name && v.Value == "1338" })
	}
	if has(plain, "CONSUL_UID") || !has(tp, "CONSUL_UID") || !has(tp, "CONSUL_GID") {
		t.Errorf("plain env = %v, transparent env = %v", plain.Environment, tp.Environment)
	}
}

func TestEnvoyUserFile(t *testing.T) {
	in := envoyInput(spec.EnvoySidecar{Inbound: &spec.EnvoyInbound{Port: 8443, AppPort: 8080}, File: "envoy.yaml"})
	in.UserFile = []byte("static_resources: {}\n")
	typ, _ := Lookup(spec.SidecarEnvoy)
	out, err := typ.RenderSidecar(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Files[0].Data) != "static_resources: {}\n" || out.Files[0].MountPath() != EnvoyConfigFile {
		t.Errorf("files = %+v", out.Files)
	}
	if len(out.AppEnv) != 1 || out.AppEnv[0].Name != "BB_LISTEN_ADDR" {
		t.Errorf("app env = %v", out.AppEnv)
	}
}

func TestPgBouncer(t *testing.T) {
	typ, _ := Lookup(spec.SidecarPgBouncer)
	sc := spec.Sidecar{Type: spec.SidecarPgBouncer, PgBouncer: &spec.PgBouncerSidecar{Service: "postgres"}}.WithDefaults()
	out, err := typ.RenderSidecar(Input{Name: "api-pgbouncer", Spec: sc, Uses: []Use{pgUse()}, Secret: secretRef,
		App: App{Service: "api", Name: "api"}})
	if err != nil {
		t.Fatal(err)
	}
	ini := map[string]string{}
	for _, line := range strings.Split(string(out.Files[0].Data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, ";") {
			ini[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	for k, v := range map[string]string{
		"orders":             "host=postgres.bb.internal port=5432 dbname=orders",
		"listen_addr":        "127.0.0.1",
		"listen_port":        "6432",
		"pool_mode":          "transaction",
		"default_pool_size":  "10",
		"auth_type":          "scram-sha-256",
		"server_tls_sslmode": "verify-full",
		"server_tls_ca_file": catalog.CAFile,
	} {
		if ini[k] != v {
			t.Errorf("pgbouncer.ini %s = %q, want %q", k, ini[k], v)
		}
	}
	if strings.Contains(string(out.Files[0].Data), "${postgres/password}") {
		t.Error("the password is in pgbouncer.ini")
	}
	env := map[string]catalog.EnvVar{}
	for _, v := range out.AppEnv {
		env[v.Name] = v
	}
	if u := env["DATABASE_URL"]; u.Value != "postgres://app:${postgres/password}@127.0.0.1:6432/orders?sslmode=disable" || !u.Secret {
		t.Errorf("DATABASE_URL = %+v", u)
	}
	if env["PGHOST"].Value != "127.0.0.1" || env["PGPORT"].Value != "6432" || env["PGSSLMODE"].Value != "disable" || len(env) != 4 {
		t.Errorf("app env = %v", env)
	}
	if !slices.Equal(out.Health.Test, []string{"CMD", "pg_isready", "-q", "-h", "127.0.0.1", "-p", "6432"}) || !slices.Equal(out.Needs, []string{"postgres"}) {
		t.Errorf("health = %v, needs = %v", out.Health.Test, out.Needs)
	}
}

func TestFluentBit(t *testing.T) {
	typ, _ := Lookup(spec.SidecarFluentBit)
	sc := spec.Sidecar{Type: spec.SidecarFluentBit, FluentBit: &spec.FluentBitSidecar{To: "s3", Bucket: "logs"}}.WithDefaults()
	s3 := Use{Service: "s3", Type: "s3-compatible", FQDN: "s3.bb.internal", Port: 8333, TLS: true,
		Env: []catalog.EnvVar{{Name: "S3_REGION", Value: "us-east-1"}}}
	out, err := typ.RenderSidecar(Input{Name: "api-2-fluent-bit", Spec: sc, Uses: []Use{s3}, Secret: secretRef,
		App: App{Service: "api-2", Name: "api", Instance: 2}})
	if err != nil {
		t.Fatal(err)
	}
	conf := string(out.Files[0].Data)
	for _, s := range []string{
		"Path                 /var/run/bb/logs/*.log",
		"endpoint             https://s3.bb.internal:8333",
		"bucket               logs",
		"tls.verify           On",
		"tls.verify_hostname  On",
		"tls.ca_file          " + catalog.CAFile,
		"s3_key_format        /api-2/",
		"Health_Check         On",
		"HTTP_Listen          127.0.0.1",
	} {
		if !strings.Contains(conf, s) {
			t.Errorf("fluent-bit.conf lacks %q:\n%s", s, conf)
		}
	}
	if strings.Contains(conf, "${s3/") {
		t.Error("a secret is in fluent-bit.conf")
	}
	if len(out.AppEnv) != 1 || out.AppEnv[0] != (catalog.EnvVar{Name: "BB_LOG_DIR", Value: LogDir}) || !slices.Equal(out.Needs, []string{"s3"}) {
		t.Errorf("app env = %v, needs = %v", out.AppEnv, out.Needs)
	}
	if !strings.Contains(strings.Join(out.Health.Test, " "), "/dev/tcp/127.0.0.1/2020") {
		t.Errorf("health = %v", out.Health.Test)
	}
}

func TestTargetEnvAndDurations(t *testing.T) {
	if got := TargetEnv("order-service"); got != "ORDER_SERVICE_URL" {
		t.Errorf("TargetEnv = %s", got)
	}
	for in, want := range map[string]string{"5s": "5s", "1m30s": "90s", "250ms": "0.25s"} {
		if got, err := protoDuration(in); err != nil || got != want {
			t.Errorf("protoDuration(%s) = %s %v, want %s", in, got, err, want)
		}
	}
}
