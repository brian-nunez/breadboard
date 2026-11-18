package core

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
)

// Proxy explorers read a running proxy's own view of its upstreams, like
// the other explorers (ADR-0012): each runs inside the proxy's container
// against its loopback-only admin or status endpoint, and the core parses
// what it prints. They show how traffic flows through the proxy: which
// backends exist, whether each is up, and how many requests each got,
// where the proxy counts that.

// exploreInfra resolves an infra service of a catalog type, like explore,
// and also returns the model.
func (e *Engine) exploreInfra(ctx context.Context, req ExploreRequest, typ string) (*model.Model, *model.Service, func(...string) ([]byte, error), error) {
	if err := e.rt.Available(ctx); err != nil {
		return nil, nil, nil, err
	}

	p, err := e.open(req.Dir)
	if err != nil {
		return nil, nil, nil, err
	}

	st, err := e.settings(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	m, err := model.Resolve(p.spec, st.Domain)
	if err != nil {
		return nil, nil, nil, err
	}

	svc, ok := m.Service(req.Service)
	if !ok || svc.Kind != model.Infra || svc.Entry.ID != typ {
		return nil, nil, nil, fmt.Errorf("no %s service named %q: %w", typ, req.Service, ErrNotFound)
	}

	run := func(cmd ...string) ([]byte, error) {
		return e.rt.Exec(ctx, m.Name, svc.Name, cmd)
	}

	return m, svc, run, nil
}

// ProxyUpstream identifies the upstream a proxy's backend balances, as
// experiment.yaml declares it. It is empty for a backend Breadboard did
// not generate (a user-owned file names its own).
type ProxyUpstream struct {
	Route  string // the route name: /<Route>/ with several HTTP upstreams
	Target string // the app or service behind it
	Mode   string // spec.TLSOn (re-encrypt), spec.TLSOff (terminate) or spec.TLSPassthrough
}

// proxyUnits maps the name each upstream's backend has in the generated
// config (catalog.Proxy.Unit) to the upstream.
func proxyUnits(svc *model.Service) map[string]ProxyUpstream {
	out := map[string]ProxyUpstream{}
	if svc.Entry.Proxy == nil || svc.Entry.Proxy.Unit == nil {
		return out
	}

	for _, u := range svc.Upstreams {
		out[svc.Entry.Proxy.Unit(u)] = ProxyUpstream{Route: u.Name, Target: u.Service, Mode: u.Mode}
	}

	return out
}

// upstreamHosts lists every name a proxy's upstreams connect to, one per
// instance on the hop's network (web-1.<domain>, api-1.plain.<domain>),
// for resolving inside the proxy.
func upstreamHosts(svc *model.Service) []string {
	var names []string

	for _, u := range svc.Upstreams {
		for _, s := range upstreamServers(u) {
			if !slices.Contains(names, s) {
				names = append(names, s)
			}
		}
	}

	return names
}

// upstreamServers are the names of an upstream's instances on the hop's
// network, or its one shared name.
func upstreamServers(u catalog.Upstream) []string {
	if len(u.Servers) > 0 {
		return u.Servers
	}

	return []string{u.Host}
}

// instanceName turns a resolved name into the instance it belongs to:
// web-2.<domain> and web-2.plain.<domain> are both web-2.
func instanceName(name, domain string) string {
	name = strings.TrimSuffix(name, ".")
	if n, ok := strings.CutSuffix(name, ".plain."+domain); ok {
		return n
	}

	return strings.TrimSuffix(name, "."+domain)
}

// parseHosts reads getent hosts output into address -> instance.
func parseHosts(out, domain string) map[string]string {
	hosts := map[string]string{}

	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			hosts[f[0]] = instanceName(f[1], domain)
		}
	}

	return hosts
}

// --- HAProxy ----------------------------------------------------------------

// HAProxyStats is HAProxy's stats page: every frontend, and every backend
// with its servers.
type HAProxyStats struct {
	Service   string
	Frontends []HAProxyFrontend
	Backends  []HAProxyBackend
}

// HAProxyFrontend is a listener HAProxy accepts connections on.
type HAProxyFrontend struct {
	Name   string
	Mode   string // http or tcp
	Status string // OPEN, STOP
	// Sessions: current, and total since HAProxy started.
	CurrentSessions int64
	TotalSessions   int64
	Requests        int64 // HTTP requests (req_tot); zero in tcp mode
	BytesIn         int64
	BytesOut        int64
}

// HAProxyBackend is a pool of servers with the balancing algorithm and
// totals over its servers.
type HAProxyBackend struct {
	Name     string
	Upstream ProxyUpstream
	Mode     string // http or tcp
	Status   string // UP when a server is up, DOWN otherwise
	Balance  string // roundrobin, leastconn, ...
	// Totals over the backend.
	CurrentSessions int64
	TotalSessions   int64
	BytesIn         int64
	BytesOut        int64
	Servers         []HAProxyServer
}

// HAProxyServer is one server of a backend: with server-template, one
// slot that holds whichever address DNS returned for it.
type HAProxyServer struct {
	Name string // the slot, e.g. web1
	// Address is ip:port, empty while the slot has no address;
	// Instance is the replica or service at that address, from DNS inside
	// the proxy. Slot numbers and replica numbers need not match.
	Address  string
	Instance string
	// Status is HAProxy's word: UP, DOWN, NOLB, MAINT, "MAINT
	// (resolution)" for a slot DNS left empty, "UP 1/2" while rising, ...
	Status string
	Up     bool
	// The last health check: its code (L4OK, L6OK, L7OK, L4CON, L7STS,
	// ...), its description, and how long it took.
	CheckStatus   string
	CheckDesc     string
	CheckMillis   int64
	CheckFailures int64 // failed checks (chkfail)
	Downs         int64 // UP to DOWN transitions (chkdown)
	LastChange    int64 // seconds since the status last changed
	Weight        int64
	// Traffic: sessions now and since start, how often the balancer
	// picked it, and bytes. In http mode a session is a request.
	CurrentSessions int64
	TotalSessions   int64
	Selected        int64 // lbtot
	BytesIn         int64
	BytesOut        int64
	ConnectErrors   int64 // econ
	ResponseErrors  int64 // eresp
	HTTP5xx         int64 // responses with a 5xx status
}

// haproxyScript reads the stats page as CSV on the loopback monitor
// frontend over bash's /dev/tcp (the image has no curl), then resolves the
// names given as arguments with the proxy's own resolver, so slot
// addresses map to instances.
var haproxyScript = fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/%d && printf 'GET %s;csv HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n' >&3 && echo '### stats' && cat <&3; exec 3<&-
echo; echo '### hosts'; [ $# -eq 0 ] || getent hosts "$@" || true`, catalog.HAProxyMonitorPort, catalog.HAProxyStatsURI)

// HAProxyStats reads an HAProxy service's stats page (per backend and
// server: status, last check, sessions, bytes) from 127.0.0.1:8404.
func (e *Engine) HAProxyStats(ctx context.Context, req ExploreRequest) (*HAProxyStats, error) {
	m, svc, run, err := e.exploreInfra(ctx, req, "haproxy")
	if err != nil {
		return nil, err
	}

	out, err := run(append([]string{"bash", "-c", haproxyScript, "bb-haproxy"}, upstreamHosts(svc)...)...)
	if err != nil {
		return nil, err
	}

	res, err := parseHAProxy(string(out), m.Domain, proxyUnits(svc))
	if err != nil {
		return nil, err
	}

	res.Service = svc.Name

	return res, nil
}

// parseHAProxy parses haproxyScript's output. Columns are read by name
// from the CSV header, so columns added by later versions do not matter.
func parseHAProxy(out, domain string, units map[string]ProxyUpstream) (*HAProxyStats, error) {
	sections := splitSections(out)

	body, err := httpBody(sections["stats"])
	if err != nil {
		return nil, fmt.Errorf("haproxy %s;csv: %w", catalog.HAProxyStatsURI, err)
	}

	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "# ")))
	r.FieldsPerRecord, r.LazyQuotes = -1, true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("haproxy stats CSV: %w", err)
	}

	if len(records) == 0 || !slices.Contains(records[0], "pxname") || !slices.Contains(records[0], "svname") {
		return nil, fmt.Errorf("haproxy stats CSV: no header")
	}

	col := map[string]int{}
	for i, h := range records[0] {
		col[h] = i
	}

	hosts := parseHosts(sections["hosts"], domain)
	res := &HAProxyStats{}
	backends := map[string]int{}

	backend := func(name string) *HAProxyBackend {
		i, ok := backends[name]
		if !ok {
			i = len(res.Backends)
			backends[name] = i
			res.Backends = append(res.Backends, HAProxyBackend{Name: name, Upstream: units[name]})
		}

		return &res.Backends[i]
	}
	for _, rec := range records[1:] {
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return rec[i]
			}

			return ""
		}
		num := func(name string) int64 {
			n, _ := strconv.ParseInt(get(name), 10, 64)

			return n
		}

		px, sv := get("pxname"), get("svname")
		switch sv {
		case "":
			continue
		case "FRONTEND":
			res.Frontends = append(res.Frontends, HAProxyFrontend{
				Name: px, Mode: get("mode"), Status: get("status"),
				CurrentSessions: num("scur"), TotalSessions: num("stot"), Requests: num("req_tot"),
				BytesIn: num("bin"), BytesOut: num("bout"),
			})
		case "BACKEND":
			b := backend(px)
			b.Mode, b.Status, b.Balance = get("mode"), get("status"), get("algo")
			b.CurrentSessions, b.TotalSessions, b.BytesIn, b.BytesOut = num("scur"), num("stot"), num("bin"), num("bout")
		default:
			s := HAProxyServer{
				Name: sv, Address: get("addr"), Status: get("status"),
				CheckStatus: strings.TrimPrefix(get("check_status"), "* "), CheckDesc: get("check_desc"),
				CheckMillis: num("check_duration"), CheckFailures: num("chkfail"), Downs: num("chkdown"),
				LastChange: num("lastchg"), Weight: num("weight"),
				CurrentSessions: num("scur"), TotalSessions: num("stot"), Selected: num("lbtot"),
				BytesIn: num("bin"), BytesOut: num("bout"),
				ConnectErrors: num("econ"), ResponseErrors: num("eresp"), HTTP5xx: num("hrsp_5xx"),
			}
			// A slot without an address reads 0.0.0.0:0 or is empty.
			if host, _, ok := strings.Cut(s.Address, ":"); !ok || host == "0.0.0.0" {
				s.Address = ""
			} else {
				s.Instance = hosts[host]
			}

			s.Up = strings.HasPrefix(s.Status, "UP") || s.Status == "no check" || s.Status == "NOLB"
			b := backend(px)
			b.Servers = append(b.Servers, s)
		}
	}

	if len(res.Frontends) == 0 && len(res.Backends) == 0 {
		return nil, fmt.Errorf("haproxy stats CSV: no rows")
	}

	return res, nil
}

// --- Traefik ----------------------------------------------------------------

// TraefikRoutes is Traefik's runtime routing configuration from its API:
// routers (which requests go where) and services (where they end up).
// Traefik's own routers and services (@internal: the API, the dashboard,
// ping) are left out.
type TraefikRoutes struct {
	Service  string
	Routers  []TraefikRouter
	Services []TraefikService
}

// TraefikRouter matches requests (HTTP) or connections (TCP, by SNI) and
// sends them to a service.
type TraefikRouter struct {
	Name        string // as Traefik names it, with its provider: web@file
	Protocol    string // http or tcp
	EntryPoints []string
	Rule        string
	Priority    int64
	Middlewares []string
	Service     string
	// TLS is how the router treats TLS: "terminate" (it decrypts),
	// "passthrough" (TCP, forwarded encrypted), or empty.
	TLS    string
	Status string // enabled, disabled, warning
	Errors []string
}

// TraefikService is a load balancer over servers.
type TraefikService struct {
	Name     string
	Upstream ProxyUpstream
	Protocol string // http or tcp
	Type     string // loadbalancer, weighted, mirroring, failover
	Strategy string // wrr, p2c, hrw, leasttime (HTTP)
	Status   string
	UsedBy   []string
	// HealthCheck summarises the active check, empty without one.
	HealthCheck string
	Servers     []TraefikServer
	Errors      []string
}

// TraefikServer is one server of a service with the status Traefik's
// health check gives it. Traefik keeps no per-server request counter in
// its API; its access log has each request.
type TraefikServer struct {
	Address  string // a URL (HTTP) or host:port (TCP)
	Instance string // the replica or service, from the server's name
	Status   string // UP or DOWN; empty when Traefik reports none
}

// traefikScript reads the API on the loopback entrypoint with the image's
// wget, one section per endpoint.
var traefikScript = fmt.Sprintf(`for p in http/routers http/services tcp/routers tcp/services; do echo "### $p"; wget -qO- "http://127.0.0.1:%d/api/$p"; echo; done`, catalog.TraefikAPIPort)

// TraefikRoutes reads a Traefik service's routers and services, with each
// server's status, from its API on the loopback entrypoint.
func (e *Engine) TraefikRoutes(ctx context.Context, req ExploreRequest) (*TraefikRoutes, error) {
	m, svc, run, err := e.exploreInfra(ctx, req, "traefik")
	if err != nil {
		return nil, err
	}

	out, err := run("sh", "-c", traefikScript)
	if err != nil {
		return nil, err
	}

	res, err := parseTraefik(string(out), m.Domain, proxyUnits(svc))
	if err != nil {
		return nil, err
	}

	res.Service = svc.Name

	return res, nil
}

type traefikRouterJSON struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	EntryPoints []string `json:"entryPoints"`
	Rule        string   `json:"rule"`
	Priority    int64    `json:"priority"`
	Middlewares []string `json:"middlewares"`
	Service     string   `json:"service"`
	TLS         *struct {
		Passthrough bool `json:"passthrough"`
	} `json:"tls"`
	Status string   `json:"status"`
	Error  []string `json:"error"`
}

type traefikServiceJSON struct {
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Type         string            `json:"type"`
	Status       string            `json:"status"`
	UsedBy       []string          `json:"usedBy"`
	ServerStatus map[string]string `json:"serverStatus"`
	Error        []string          `json:"error"`
	LoadBalancer *struct {
		Strategy string `json:"strategy"`
		Servers  []struct {
			URL     string `json:"url"`
			Address string `json:"address"`
		} `json:"servers"`
		HealthCheck *struct {
			Path     string `json:"path"`
			Interval string `json:"interval"`
			Timeout  string `json:"timeout"`
		} `json:"healthCheck"`
	} `json:"loadBalancer"`
}

// parseTraefik parses traefikScript's output.
func parseTraefik(out, domain string, units map[string]ProxyUpstream) (*TraefikRoutes, error) {
	sections := splitSections(out)
	res := &TraefikRoutes{}

	for _, proto := range []string{"http", "tcp"} {
		var routers []traefikRouterJSON
		if err := traefikJSON(sections, proto+"/routers", &routers); err != nil {
			return nil, err
		}

		for _, r := range routers {
			if r.Provider == "internal" {
				continue
			}

			tr := TraefikRouter{
				Name: r.Name, Protocol: proto, EntryPoints: r.EntryPoints, Rule: r.Rule, Priority: r.Priority,
				Middlewares: r.Middlewares, Service: r.Service, Status: r.Status, Errors: r.Error,
			}
			switch {
			case r.TLS != nil && r.TLS.Passthrough:
				tr.TLS = "passthrough"
			case r.TLS != nil:
				tr.TLS = "terminate"
			}

			res.Routers = append(res.Routers, tr)
		}

		var services []traefikServiceJSON
		if err := traefikJSON(sections, proto+"/services", &services); err != nil {
			return nil, err
		}

		for _, s := range services {
			if s.Provider == "internal" {
				continue
			}

			ts := TraefikService{
				Name: s.Name, Upstream: units[strings.TrimSuffix(s.Name, "@"+s.Provider)], Protocol: proto,
				Type: s.Type, Status: s.Status, UsedBy: s.UsedBy, Errors: s.Error,
			}
			if lb := s.LoadBalancer; lb != nil {
				ts.Strategy = lb.Strategy
				if hc := lb.HealthCheck; hc != nil {
					ts.HealthCheck = strings.TrimSpace(fmt.Sprintf("every %s %s", hc.Interval, cmp.Or(hc.Path, "(TCP connect)")))
				}

				for _, sv := range lb.Servers {
					addr := cmp.Or(sv.URL, sv.Address)
					ts.Servers = append(ts.Servers, TraefikServer{Address: addr, Instance: instanceName(serverHost(addr), domain), Status: s.ServerStatus[addr]})
				}
			}

			res.Services = append(res.Services, ts)
		}
	}

	return res, nil
}

// traefikJSON decodes one section of traefikScript's output.
func traefikJSON(sections map[string]string, name string, v any) error {
	body := strings.TrimSpace(sections[name])
	if body == "" {
		return fmt.Errorf("traefik /api/%s: no response", name)
	}

	if err := json.Unmarshal([]byte(body), v); err != nil {
		return fmt.Errorf("traefik /api/%s: %w", name, err)
	}

	return nil
}

// serverHost is the host of a server URL or host:port.
func serverHost(addr string) string {
	if _, rest, ok := strings.Cut(addr, "://"); ok {
		addr = rest
	}

	addr, _, _ = strings.Cut(addr, "/")
	host, _, _ := strings.Cut(addr, ":")

	return host
}

// --- NGINX ------------------------------------------------------------------

// NginxStatus is what open-source NGINX reports about itself: the
// stub_status counters, which cover the whole server. Per-upstream and
// per-server counters are an NGINX Plus feature, so Backends lists what
// each upstream resolves to from inside the container instead, which is
// where NGINX sends traffic.
type NginxStatus struct {
	Service string
	// Connections now: all of them, and those reading a request, writing a
	// response, and idle keep-alive connections waiting for one.
	Active  int64
	Reading int64
	Writing int64
	Waiting int64
	// Totals since NGINX started: connections accepted and handled
	// (fewer handled means a limit was hit), and requests.
	Accepts   int64
	Handled   int64
	Requests  int64
	Upstreams []NginxUpstream
}

// NginxUpstream is one upstream with the instances behind it.
type NginxUpstream struct {
	ProxyUpstream
	Port     int
	Backends []NginxBackend
}

// NginxBackend is one instance of an upstream and the address its name
// resolves to inside the container; empty when it does not resolve (the
// instance is not running).
type NginxBackend struct {
	Instance string
	Name     string
	Address  string
}

// nginxScript reads stub_status on the loopback status server with the
// image's wget, then resolves the names given as arguments one by one:
// musl's getent stops at the first name that does not resolve.
var nginxScript = fmt.Sprintf(`echo '### stub_status'; wget -qO- http://127.0.0.1:%d%s
echo '### hosts'; for n in "$@"; do getent hosts "$n" || true; done`, catalog.NginxStatusPort, catalog.NginxStatusURI)

// NginxStatus reads an NGINX service's stub_status counters from
// 127.0.0.1:8404 and resolves its upstreams' instances.
func (e *Engine) NginxStatus(ctx context.Context, req ExploreRequest) (*NginxStatus, error) {
	m, svc, run, err := e.exploreInfra(ctx, req, "nginx")
	if err != nil {
		return nil, err
	}

	names := upstreamHosts(svc)

	out, err := run(append([]string{"sh", "-c", nginxScript, "bb-nginx"}, names...)...)
	if err != nil {
		return nil, err
	}

	res, err := parseNginx(string(out))
	if err != nil {
		return nil, err
	}

	res.Service = svc.Name
	byName := map[string]string{}

	for _, line := range strings.Split(splitSections(string(out))["hosts"], "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			byName[strings.TrimSuffix(f[1], ".")] = f[0]
		}
	}

	for _, u := range svc.Upstreams {
		nu := NginxUpstream{ProxyUpstream: ProxyUpstream{Route: u.Name, Target: u.Service, Mode: u.Mode}, Port: u.Port}
		for _, s := range upstreamServers(u) {
			nu.Backends = append(nu.Backends, NginxBackend{Instance: instanceName(s, m.Domain), Name: s, Address: byName[s]})
		}

		res.Upstreams = append(res.Upstreams, nu)
	}

	return res, nil
}

// parseNginx parses the stub_status section of nginxScript's output:
//
//	Active connections: 1
//	server accepts handled requests
//	 18 18 55
//	Reading: 0 Writing: 1 Waiting: 0
func parseNginx(out string) (*NginxStatus, error) {
	text := splitSections(out)["stub_status"]
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	res := &NginxStatus{}

	var found int

	for i, line := range lines {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "Active" && f[1] == "connections:":
			res.Active, _ = strconv.ParseInt(f[2], 10, 64)
			found++
		case len(f) == 4 && f[0] == "server" && f[1] == "accepts" && i+1 < len(lines):
			n := strings.Fields(lines[i+1])
			if len(n) >= 3 {
				res.Accepts, _ = strconv.ParseInt(n[0], 10, 64)
				res.Handled, _ = strconv.ParseInt(n[1], 10, 64)
				res.Requests, _ = strconv.ParseInt(n[2], 10, 64)
				found++
			}
		case len(f) == 6 && f[0] == "Reading:":
			res.Reading, _ = strconv.ParseInt(f[1], 10, 64)
			res.Writing, _ = strconv.ParseInt(f[3], 10, 64)
			res.Waiting, _ = strconv.ParseInt(f[5], 10, 64)
			found++
		}
	}

	if found != 3 {
		return nil, fmt.Errorf("nginx %s: unexpected output %q", catalog.NginxStatusURI, strings.TrimSpace(text))
	}

	return res, nil
}

// --- Infra Envoy and PgBouncer ----------------------------------------------

// envoyInfra reads an infra Envoy proxy's clusters: one per upstream,
// named as the generated config names them (catalog.Proxy.Unit).
func (e *Engine) envoyInfra(m *model.Model, svc *model.Service, run func(...string) ([]byte, error)) (*EnvoyClusters, error) {
	out, err := run(append([]string{"bash", "-c", envoyAdminScript, "bb-envoy"}, upstreamHosts(svc)...)...)
	if err != nil {
		return nil, err
	}

	res, err := parseEnvoy(string(out), "."+m.Domain)
	if err != nil {
		return nil, err
	}

	res.Proxy = svc.Name
	units := proxyUnits(svc)

	for i := range res.Clusters {
		c := &res.Clusters[i]
		if u, ok := units[c.Name]; ok {
			c.Role, c.Target, c.Mode = "upstream", u.Target, u.Mode
		}

		for j := range c.Hosts {
			// Terminate hops resolve as <instance>.plain.<domain>.
			c.Hosts[j].Instance = strings.TrimSuffix(c.Hosts[j].Instance, ".plain")
		}
	}

	return res, nil
}

// pgbouncerInfra reads an infra PgBouncer's pools: the same script as the
// sidecar's, on the pool's port, as the role the pool already holds.
func (e *Engine) pgbouncerInfra(svc *model.Service, run func(...string) ([]byte, error)) (*PgBouncerPools, error) {
	out, err := run("sh", "-c", pgbouncerScript, "bb-pgbouncer", strconv.Itoa(catalog.PgBouncerPort))
	if err != nil {
		return nil, err
	}

	pools, err := parsePgBouncer(string(out))
	if err != nil {
		return nil, err
	}

	res := &PgBouncerPools{Proxy: svc.Name, PoolMode: svc.Config["pool_mode"], Port: catalog.PgBouncerPort, Pools: pools}

	res.PoolSize, _ = strconv.Atoi(svc.Config["pool_size"])
	if len(svc.Upstreams) > 0 {
		res.Service = svc.Upstreams[0].Service
	}

	return res, nil
}

// infraProxy resolves req.Service to an infra service of the type, for
// the explorers that read a sidecar or an infra service of the same
// software. ok is false when it names no such service.
func (e *Engine) infraProxy(ctx context.Context, req ExploreRequest, typ string) (m *model.Model, svc *model.Service, run func(...string) ([]byte, error), ok bool, err error) {
	m, svc, run, err = e.exploreInfra(ctx, req, typ)
	switch {
	case err == nil:
		return m, svc, run, true, nil
	case errors.Is(err, ErrNotFound):
		return nil, nil, nil, false, nil
	}

	return nil, nil, nil, false, err
}
