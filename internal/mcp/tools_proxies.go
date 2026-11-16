package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Proxy views: a proxy's upstreams in service_details, and the explorers
// for HAProxy, Traefik and NGINX. All of them are read-only.

type upstream struct {
	Name       string `json:"name" jsonschema:"the route name"`
	Service    string `json:"service" jsonschema:"the app or service behind the proxy"`
	App        bool   `json:"app"`
	Instances  int    `json:"instances" jsonschema:"replicas the proxy balances across"`
	Mode       string `json:"mode" jsonschema:"on (re-encrypt: TLS to the backend, verified), off (terminate: plaintext on a private segment) or passthrough (the client's TLS session, routed by SNI)"`
	Reason     string `json:"reason,omitempty" jsonschema:"why a terminate hop is plaintext"`
	Port       int    `json:"port" jsonschema:"the target port: TLS, or plaintext for terminate"`
	ListenPort int    `json:"listen_port" jsonschema:"the proxy port the route arrives on"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"requests under this prefix go here, prefix stripped; empty when this upstream gets every request"`
	Default    bool   `json:"default" jsonschema:"also takes every request no prefix matches"`
	SNI        string `json:"sni,omitempty" jsonschema:"passthrough: the TLS server name routed here"`
	Route      string `json:"route" jsonschema:"the route in words"`
}

func upstreamView(u core.UpstreamInfo) upstream {
	return upstream{
		Name: u.Name, Service: u.Service, App: u.App, Instances: u.Instances, Mode: u.Mode, Reason: u.Reason, Port: u.Port,
		ListenPort: u.ListenPort, PathPrefix: u.PathPrefix, Default: u.Default, SNI: u.SNI, Route: u.Route,
	}
}

type proxyUpstream struct {
	Route  string `json:"route,omitempty" jsonschema:"the upstream's route name in experiment.yaml; empty for a backend a user-owned file added"`
	Target string `json:"target,omitempty" jsonschema:"the app or service behind it"`
	Mode   string `json:"mode,omitempty" jsonschema:"on (re-encrypt), off (terminate) or passthrough"`
}

func proxyUpstreamView(u core.ProxyUpstream) proxyUpstream {
	return proxyUpstream{Route: u.Route, Target: u.Target, Mode: u.Mode}
}

type haproxyFrontend struct {
	Name            string `json:"name"`
	Mode            string `json:"mode" jsonschema:"http or tcp"`
	Status          string `json:"status"`
	CurrentSessions int64  `json:"current_sessions"`
	TotalSessions   int64  `json:"total_sessions"`
	Requests        int64  `json:"requests" jsonschema:"HTTP requests; zero in tcp mode"`
	BytesIn         int64  `json:"bytes_in"`
	BytesOut        int64  `json:"bytes_out"`
}

type haproxyServer struct {
	Name            string `json:"name" jsonschema:"the server-template slot, e.g. web1; slot numbers are not replica numbers"`
	Address         string `json:"address,omitempty" jsonschema:"ip:port; empty while DNS gave the slot no address"`
	Instance        string `json:"instance,omitempty" jsonschema:"the replica or service at that address"`
	Status          string `json:"status" jsonschema:"UP, DOWN, NOLB, MAINT, MAINT (resolution) for an empty slot, ..."`
	Up              bool   `json:"up"`
	CheckStatus     string `json:"check_status,omitempty" jsonschema:"last health check: L4OK, L6OK, L7OK, L4CON, L7STS, ..."`
	CheckDesc       string `json:"check_desc,omitempty"`
	CheckMillis     int64  `json:"check_ms"`
	CheckFailures   int64  `json:"check_failures"`
	Downs           int64  `json:"downs" jsonschema:"UP to DOWN transitions"`
	LastChange      int64  `json:"last_change_s" jsonschema:"seconds since the status changed"`
	Weight          int64  `json:"weight"`
	CurrentSessions int64  `json:"current_sessions"`
	TotalSessions   int64  `json:"total_sessions" jsonschema:"in http mode, one per request"`
	Selected        int64  `json:"selected" jsonschema:"times the balancer picked this server"`
	BytesIn         int64  `json:"bytes_in"`
	BytesOut        int64  `json:"bytes_out"`
	ConnectErrors   int64  `json:"connect_errors"`
	ResponseErrors  int64  `json:"response_errors"`
	HTTP5xx         int64  `json:"http_5xx"`
}

type haproxyBackend struct {
	Name            string          `json:"name"`
	Upstream        proxyUpstream   `json:"upstream"`
	Mode            string          `json:"mode" jsonschema:"http or tcp"`
	Status          string          `json:"status"`
	Balance         string          `json:"balance" jsonschema:"the balancing algorithm"`
	CurrentSessions int64           `json:"current_sessions"`
	TotalSessions   int64           `json:"total_sessions"`
	BytesIn         int64           `json:"bytes_in"`
	BytesOut        int64           `json:"bytes_out"`
	Servers         []haproxyServer `json:"servers"`
}

type haproxyOut struct {
	Service   string            `json:"service"`
	Frontends []haproxyFrontend `json:"frontends"`
	Backends  []haproxyBackend  `json:"backends"`
}

type traefikRouter struct {
	Name        string   `json:"name"`
	Protocol    string   `json:"protocol" jsonschema:"http or tcp"`
	EntryPoints []string `json:"entry_points"`
	Rule        string   `json:"rule"`
	Priority    int64    `json:"priority"`
	Middlewares []string `json:"middlewares"`
	Service     string   `json:"service"`
	TLS         string   `json:"tls,omitempty" jsonschema:"terminate (Traefik decrypts) or passthrough"`
	Status      string   `json:"status"`
	Errors      []string `json:"errors"`
}

type traefikServer struct {
	Address  string `json:"address" jsonschema:"a URL (HTTP) or host:port (TCP)"`
	Instance string `json:"instance,omitempty" jsonschema:"the replica or service"`
	Status   string `json:"status,omitempty" jsonschema:"UP or DOWN from Traefik's health check"`
}

type traefikService struct {
	Name        string          `json:"name"`
	Upstream    proxyUpstream   `json:"upstream"`
	Protocol    string          `json:"protocol" jsonschema:"http or tcp"`
	Type        string          `json:"type,omitempty"`
	Strategy    string          `json:"strategy,omitempty" jsonschema:"wrr, p2c, hrw or leasttime"`
	Status      string          `json:"status"`
	UsedBy      []string        `json:"used_by"`
	HealthCheck string          `json:"health_check,omitempty"`
	Servers     []traefikServer `json:"servers"`
	Errors      []string        `json:"errors"`
}

type traefikOut struct {
	Service  string           `json:"service"`
	Routers  []traefikRouter  `json:"routers"`
	Services []traefikService `json:"services"`
}

type nginxBackend struct {
	Instance string `json:"instance"`
	Name     string `json:"name" jsonschema:"the DNS name NGINX connects to"`
	Address  string `json:"address,omitempty" jsonschema:"what it resolves to inside NGINX; empty when it does not resolve"`
}

type nginxUpstream struct {
	Route    string         `json:"route"`
	Target   string         `json:"target"`
	Mode     string         `json:"mode"`
	Port     int            `json:"port"`
	Backends []nginxBackend `json:"backends"`
}

type nginxOut struct {
	Service   string          `json:"service"`
	Active    int64           `json:"active_connections"`
	Reading   int64           `json:"reading"`
	Writing   int64           `json:"writing"`
	Waiting   int64           `json:"waiting" jsonschema:"idle keep-alive connections"`
	Accepts   int64           `json:"accepts"`
	Handled   int64           `json:"handled" jsonschema:"fewer than accepts means a limit was hit"`
	Requests  int64           `json:"requests"`
	Upstreams []nginxUpstream `json:"upstreams" jsonschema:"open-source NGINX has no per-server counters; these are the instances each upstream resolves to"`
}

func (s *Server) addProxyExplorers(explore func(exploreIn) (core.ExploreRequest, error)) {
	add(s, readTool, true, &sdk.Tool{
		Name:        "haproxy_stats",
		Description: "A running HAProxy service's stats page (CSV on its loopback monitor frontend, read inside its container): frontends with sessions and requests, and each backend with the upstream it balances (route, target, TLS mode) and its servers (the replica at each address, UP/DOWN, last health check, sessions, picks, bytes, errors). Use it to see how requests spread over replicas and why one is down.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (haproxyOut, error) {
		req, err := explore(in)
		if err != nil {
			return haproxyOut{}, err
		}
		res, err := s.eng.HAProxyStats(ctx, req)
		if err != nil {
			return haproxyOut{}, err
		}
		return haproxyOut{
			Service: res.Service,
			Frontends: mapSlice(res.Frontends, func(f core.HAProxyFrontend) haproxyFrontend {
				return haproxyFrontend{Name: f.Name, Mode: f.Mode, Status: f.Status, CurrentSessions: f.CurrentSessions, TotalSessions: f.TotalSessions, Requests: f.Requests, BytesIn: f.BytesIn, BytesOut: f.BytesOut}
			}),
			Backends: mapSlice(res.Backends, func(b core.HAProxyBackend) haproxyBackend {
				return haproxyBackend{
					Name: b.Name, Upstream: proxyUpstreamView(b.Upstream), Mode: b.Mode, Status: b.Status, Balance: b.Balance,
					CurrentSessions: b.CurrentSessions, TotalSessions: b.TotalSessions, BytesIn: b.BytesIn, BytesOut: b.BytesOut,
					Servers: mapSlice(b.Servers, func(v core.HAProxyServer) haproxyServer {
						return haproxyServer{
							Name: v.Name, Address: v.Address, Instance: v.Instance, Status: v.Status, Up: v.Up,
							CheckStatus: v.CheckStatus, CheckDesc: v.CheckDesc, CheckMillis: v.CheckMillis, CheckFailures: v.CheckFailures, Downs: v.Downs,
							LastChange: v.LastChange, Weight: v.Weight, CurrentSessions: v.CurrentSessions, TotalSessions: v.TotalSessions, Selected: v.Selected,
							BytesIn: v.BytesIn, BytesOut: v.BytesOut, ConnectErrors: v.ConnectErrors, ResponseErrors: v.ResponseErrors, HTTP5xx: v.HTTP5xx,
						}
					}),
				}
			}),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "traefik_routes",
		Description: "A running Traefik service's routing from its API on the loopback entrypoint, read inside its container: HTTP and TCP routers (rule, entry points, middlewares, service, TLS terminate or passthrough) and services with the upstream each balances, its strategy, health check, and each server's UP/DOWN status. Traefik's API has no per-server request counts; its access log (logs) has each request. Traefik's own @internal routers are left out.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (traefikOut, error) {
		req, err := explore(in)
		if err != nil {
			return traefikOut{}, err
		}
		res, err := s.eng.TraefikRoutes(ctx, req)
		if err != nil {
			return traefikOut{}, err
		}
		return traefikOut{
			Service: res.Service,
			Routers: mapSlice(res.Routers, func(r core.TraefikRouter) traefikRouter {
				return traefikRouter{Name: r.Name, Protocol: r.Protocol, EntryPoints: orEmpty(r.EntryPoints), Rule: r.Rule, Priority: r.Priority,
					Middlewares: orEmpty(r.Middlewares), Service: r.Service, TLS: r.TLS, Status: r.Status, Errors: orEmpty(r.Errors)}
			}),
			Services: mapSlice(res.Services, func(v core.TraefikService) traefikService {
				return traefikService{
					Name: v.Name, Upstream: proxyUpstreamView(v.Upstream), Protocol: v.Protocol, Type: v.Type, Strategy: v.Strategy, Status: v.Status,
					UsedBy: orEmpty(v.UsedBy), HealthCheck: v.HealthCheck, Errors: orEmpty(v.Errors),
					Servers: mapSlice(v.Servers, func(sv core.TraefikServer) traefikServer {
						return traefikServer{Address: sv.Address, Instance: sv.Instance, Status: sv.Status}
					}),
				}
			}),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "nginx_status",
		Description: "A running NGINX service's stub_status counters (connections active, reading, writing, waiting; accepts, handled, requests since start), read on its loopback status server inside its container, and for each upstream the instances its name resolves to there. Open-source NGINX has no per-server request counters (an NGINX Plus feature); its access log (logs) has each request.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (nginxOut, error) {
		req, err := explore(in)
		if err != nil {
			return nginxOut{}, err
		}
		res, err := s.eng.NginxStatus(ctx, req)
		if err != nil {
			return nginxOut{}, err
		}
		return nginxOut{
			Service: res.Service, Active: res.Active, Reading: res.Reading, Writing: res.Writing, Waiting: res.Waiting,
			Accepts: res.Accepts, Handled: res.Handled, Requests: res.Requests,
			Upstreams: mapSlice(res.Upstreams, func(u core.NginxUpstream) nginxUpstream {
				return nginxUpstream{Route: u.Route, Target: u.Target, Mode: u.Mode, Port: u.Port,
					Backends: mapSlice(u.Backends, func(b core.NginxBackend) nginxBackend {
						return nginxBackend{Instance: b.Instance, Name: b.Name, Address: b.Address}
					})}
			}),
		}, nil
	})
}
