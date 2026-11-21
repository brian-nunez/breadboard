package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Tools that inspect a running experiment: status, logs, credentials,
// service details, and the explorers that read service data through each
// service's own command-line tool.

const (
	defaultLogTail = 200
	maxLogTail     = 2000
)

type statusOut struct {
	Experiment string          `json:"experiment"`
	Domain     string          `json:"domain"`
	Services   []serviceStatus `json:"services"`
	Warnings   []string        `json:"warnings" jsonschema:"TLS and resource warnings"`
	HostURLs   []hostURL       `json:"host_urls,omitempty" jsonschema:"URLs that reach host targets from the host (https://<target>.<experiment>.localhost:<port>); absent without host access or before the first up"`
	HostPort   int             `json:"host_port,omitempty" jsonschema:"the 127.0.0.1 port the host router is published on"`
}

type logsIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Service string `json:"service,omitempty" jsonschema:"one service or app; empty means all of them"`
	Tail    int    `json:"tail,omitempty" jsonschema:"lines per service from the end; default 200, at most 2000"`
}

type logLine struct {
	Service string `json:"service,omitempty"`
	Text    string `json:"text"`
}

type logsOut struct {
	Lines []logLine `json:"lines"`
	Tail  int       `json:"tail" jsonschema:"lines per service that were asked for"`
}

type serviceIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Service string `json:"service" jsonschema:"service name from experiment.yaml"`
}

type credsIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Service string `json:"service,omitempty" jsonschema:"one infra service; empty means all of them"`
}

type secret struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Help  string `json:"help,omitempty"`
}

type serviceCreds struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Address string   `json:"address"`
	Secrets []secret `json:"secrets"`
	Env     []envVar `json:"env" jsonschema:"the variables an app that uses this service receives, with values"`
}

type credsOut struct {
	Services []serviceCreds `json:"services"`
}

type listener struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	Port          int    `json:"port"`
	PlaintextPort int    `json:"plaintext_port,omitempty"`
}

type envGroup struct {
	Service   string   `json:"service,omitempty" jsonschema:"the service whose contract this is; empty for Breadboard's own variables"`
	Title     string   `json:"title"`
	Plaintext bool     `json:"plaintext,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Sidecar   bool     `json:"sidecar,omitempty" jsonschema:"variables a sidecar adds; service is the sidecar"`
	Vars      []envVar `json:"vars"`
}

type certificate struct {
	Issuer   string   `json:"issuer"`
	DNSNames []string `json:"dns_names"`
	Identity string   `json:"identity" jsonschema:"SPIFFE URI"`
	NotAfter string   `json:"not_after"`
}

type serviceDetailsOut struct {
	Name        string       `json:"name"`
	Kind        string       `json:"kind" jsonschema:"infra, app, or sidecar"`
	Type        string       `json:"type,omitempty"`
	Title       string       `json:"title,omitempty"`
	Summary     string       `json:"summary,omitempty"`
	Image       string       `json:"image,omitempty"`
	FQDN        string       `json:"fqdn"`
	Address     string       `json:"address,omitempty"`
	Listeners   []listener   `json:"listeners"`
	UsedBy      []string     `json:"used_by"`
	Upstreams   []upstream   `json:"upstreams,omitempty" jsonschema:"a proxy's backends with their TLS mode and route"`
	File        string       `json:"file,omitempty" jsonschema:"a proxy's own native config, relative to experiment.yaml, replacing the generated one"`
	StaticFile  string       `json:"static_file,omitempty" jsonschema:"a proxy's static configuration, relative to experiment.yaml, merged into the generated one (traefik.yml)"`
	Env         []envGroup   `json:"env" jsonschema:"secret values are empty; call creds for them"`
	Certificate *certificate `json:"certificate,omitempty" jsonschema:"absent before the first up"`
	Path        string       `json:"path,omitempty"`
	Command     []string     `json:"command,omitempty"`
	App         string       `json:"app,omitempty" jsonschema:"apps: the app in experiment.yaml"`
	Instance    int          `json:"instance,omitempty" jsonschema:"apps: the instance described (1..N), absent with one replica"`
	Replicas    []string     `json:"replicas,omitempty" jsonschema:"apps: every instance's service name"`
	Sidecars    []sidecar    `json:"sidecars,omitempty" jsonschema:"apps: the instance's sidecars"`
	Parent      string       `json:"parent,omitempty"`
	Registry    string       `json:"registry,omitempty"`
	Sidecar     *sidecar     `json:"sidecar,omitempty" jsonschema:"sidecars: what it is"`
	HostURLs    []string     `json:"host_urls,omitempty" jsonschema:"URLs that reach it from the host through the host router"`
	HostRouter  bool         `json:"host_router,omitempty" jsonschema:"the lab's host router (bb-host), which serves every host URL"`
}

type exploreIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Service string `json:"service" jsonschema:"name of the service to explore, from experiment.yaml"`
}

type postgresTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows" jsonschema:"estimated"`
	Bytes  int64  `json:"bytes"`
}

type postgresOut struct {
	Database string          `json:"database"`
	Tables   []postgresTable `json:"tables"`
}

type redisKey struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl" jsonschema:"seconds; -1 means no expiry"`
}

type redisOut struct {
	Total     int64      `json:"total" jsonschema:"DBSIZE"`
	Keys      []redisKey `json:"keys"`
	Truncated bool       `json:"truncated" jsonschema:"more keys exist than were returned"`
}

type consulService struct {
	Name      string   `json:"name"`
	Tags      []string `json:"tags"`
	Instances int      `json:"instances"`
	Passing   int      `json:"passing"`
	Warning   int      `json:"warning"`
	Critical  int      `json:"critical"`
}

type consulNode struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type consulOut struct {
	Services []consulService `json:"services"`
	Nodes    []consulNode    `json:"nodes"`
}

type kafkaTopic struct {
	Name              string `json:"name"`
	Partitions        int    `json:"partitions"`
	ReplicationFactor int    `json:"replication_factor"`
	Messages          int64  `json:"messages"`
	Internal          bool   `json:"internal"`
}

type kafkaGroup struct {
	Name    string   `json:"name"`
	Topics  []string `json:"topics"`
	Members int      `json:"members"`
	Lag     int64    `json:"lag" jsonschema:"records written but not yet committed, over all partitions"`
}

type kafkaOut struct {
	Topics []kafkaTopic `json:"topics"`
	Groups []kafkaGroup `json:"groups"`
}

type s3Bucket struct {
	Name      string `json:"name"`
	Objects   int    `json:"objects"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated" jsonschema:"the bucket holds more objects than were counted"`
}

type s3Out struct {
	Buckets []s3Bucket `json:"buckets"`
}

type sidecarIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Service string `json:"service" jsonschema:"an infra service of the type (a proxy), an app (its first instance's sidecar), an app instance such as api-2, or the sidecar's service name such as api-2-envoy"`
}

type envoyListener struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type envoyHost struct {
	Address           string   `json:"address"`
	Hostname          string   `json:"hostname,omitempty" jsonschema:"the DNS name Envoy resolved"`
	Instance          string   `json:"instance,omitempty" jsonschema:"the app instance or service at this address: which replica got the traffic"`
	Healthy           bool     `json:"healthy"`
	Flags             []string `json:"flags" jsonschema:"why the host is not healthy, e.g. failed_outlier_check"`
	Ejected           bool     `json:"ejected" jsonschema:"ejected by outlier detection"`
	Requests          int64    `json:"requests"`
	Successes         int64    `json:"successes"`
	Errors            int64    `json:"errors" jsonschema:"5xx responses and resets"`
	Timeouts          int64    `json:"timeouts"`
	ConnectFailures   int64    `json:"connect_failures"`
	ActiveConnections int64    `json:"active_connections"`
	ActiveRequests    int64    `json:"active_requests"`
}

type envoyCluster struct {
	Name              string      `json:"name"`
	Role              string      `json:"role,omitempty" jsonschema:"outbound, inbound, egress (transparent passthrough), or inbound passthrough; upstream for an infra Envoy proxy"`
	Target            string      `json:"target,omitempty" jsonschema:"the app or service the cluster reaches"`
	Mode              string      `json:"mode,omitempty" jsonschema:"an infra proxy's upstream TLS mode: on (re-encrypt), off (terminate) or passthrough"`
	Retries           int64       `json:"retries"`
	EjectionsActive   int64       `json:"ejections_active"`
	EjectionsEnforced int64       `json:"ejections_enforced"`
	EjectionsOverflow int64       `json:"ejections_overflow" jsonschema:"ejections max_ejection_percent prevented"`
	Hosts             []envoyHost `json:"hosts"`
}

type envoyOut struct {
	Sidecar   string          `json:"sidecar,omitempty"`
	App       string          `json:"app,omitempty" jsonschema:"the app instance the sidecar serves"`
	Proxy     string          `json:"proxy,omitempty" jsonschema:"the infra Envoy service, when service named one instead of a sidecar"`
	Listeners []envoyListener `json:"listeners"`
	Clusters  []envoyCluster  `json:"clusters"`
}

type pgbouncerPool struct {
	Database       string `json:"database"`
	User           string `json:"user"`
	PoolMode       string `json:"pool_mode"`
	ClientsActive  int64  `json:"clients_active"`
	ClientsWaiting int64  `json:"clients_waiting"`
	ServersActive  int64  `json:"servers_active"`
	ServersIdle    int64  `json:"servers_idle"`
	ServersUsed    int64  `json:"servers_used"`
	ServersLogin   int64  `json:"servers_login"`
	MaxWaitMicros  int64  `json:"max_wait_us" jsonschema:"how long the oldest waiting client has waited"`
	Transactions   int64  `json:"transactions"`
	Queries        int64  `json:"queries"`
	BytesReceived  int64  `json:"bytes_received"`
	BytesSent      int64  `json:"bytes_sent"`
	AvgQueryMicros int64  `json:"avg_query_us"`
	AvgWaitMicros  int64  `json:"avg_wait_us"`
}

type pgbouncerOut struct {
	Sidecar  string          `json:"sidecar,omitempty"`
	App      string          `json:"app,omitempty"`
	Proxy    string          `json:"proxy,omitempty" jsonschema:"the infra pgbouncer service shared by apps, when service named one instead of a sidecar"`
	Service  string          `json:"service" jsonschema:"the Postgres service it pools"`
	PoolMode string          `json:"pool_mode"`
	PoolSize int             `json:"pool_size"`
	Port     int             `json:"port" jsonschema:"where apps reach it: on 127.0.0.1 for a sidecar, on the service's name for an infra pool"`
	Pools    []pgbouncerPool `json:"pools"`
}

func (s *Server) addExperimentTools() {
	add(s, readTool, true, &sdk.Tool{
		Name:        "status",
		Description: "Every service's container state, health, image, CPU and memory, and its connections, plus TLS warnings. Same as bb status.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in dirIn) (statusOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return statusOut{}, err
		}

		res, err := s.eng.Status(ctx, core.StatusRequest{Dir: dir})
		if err != nil {
			return statusOut{}, err
		}

		return statusOut{
			Experiment: res.Experiment, Domain: res.Domain, Services: mapSlice(res.Services, statusView), Warnings: orEmpty(res.Warnings),
			HostURLs: mapSlice(res.HostURLs, hostURLView), HostPort: res.HostPort,
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "logs",
		Description: "Recent log lines of one service or all of them (no follow mode). Use it to find out why a service is unhealthy or what an app is doing.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in logsIn) (logsOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return logsOut{}, err
		}

		tail := in.Tail
		if tail <= 0 {
			tail = defaultLogTail
		}

		tail = min(tail, maxLogTail)

		lines, err := s.eng.Logs(ctx, core.LogsRequest{Dir: dir, Service: in.Service, Tail: tail})
		if err != nil {
			return logsOut{}, err
		}

		out := logsOut{Lines: []logLine{}, Tail: tail}

		for l, err := range lines {
			if err != nil {
				return logsOut{}, err
			}

			out.Lines = append(out.Lines, logLine{Service: l.Service, Text: l.Text})
		}

		return out, nil
	})

	add(s, stateTool, true, &sdk.Tool{
		Name:        "creds",
		Description: "Generated credentials and connection strings for infra services, with values: users, passwords, URLs, and the exact environment an app that uses each service receives. They are local, per-experiment lab credentials. Generates any that do not exist yet. Same as bb creds.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in credsIn) (credsOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return credsOut{}, err
		}

		res, err := s.eng.Creds(ctx, core.CredsRequest{Dir: dir, Service: in.Service})
		if err != nil {
			return credsOut{}, err
		}

		return credsOut{Services: mapSlice(res.Services, func(c core.ServiceCreds) serviceCreds {
			return serviceCreds{
				Name: c.Name, Type: c.Type, Address: c.Address,
				Secrets: mapSlice(c.Secrets, func(v core.Secret) secret { return secret{Key: v.Key, Value: v.Value, Help: v.Help} }),
				Env:     mapSlice(c.Env, envVarView),
			}
		})}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "service_details",
		Description: "How one service is set up: its catalog type and image, DNS name, listeners, which apps use it, the environment involved, its certificate, and for apps the path and command.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in serviceIn) (serviceDetailsOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return serviceDetailsOut{}, err
		}

		d, err := s.eng.ServiceDetails(ctx, core.ServiceDetailsRequest{Dir: dir, Name: in.Service})
		if err != nil {
			return serviceDetailsOut{}, err
		}

		out := serviceDetailsOut{
			Name: d.Name, Kind: d.Kind, Type: d.Type, Title: d.Title, Summary: d.Summary, Image: d.Image,
			FQDN: d.FQDN, Address: d.Address,
			Listeners: mapSlice(d.Listeners, func(l core.Listener) listener {
				return listener{Name: l.Name, Protocol: l.Protocol, Port: l.Port, PlaintextPort: l.PlaintextPort}
			}),
			UsedBy:    orEmpty(d.UsedBy),
			Upstreams: mapSlice(d.Upstreams, upstreamView),
			File:      d.File, StaticFile: d.StaticFile,
			Env: mapSlice(d.Env, func(g core.EnvGroup) envGroup {
				return envGroup{Service: g.Service, Title: g.Title, Plaintext: g.Plaintext, Reason: g.Reason, Sidecar: g.Sidecar, Vars: mapSlice(g.Vars, envVarView)}
			}),
			Path: d.Path, Command: d.Command,
			App: d.App, Instance: d.Instance, Replicas: d.Replicas, Sidecars: mapSlice(d.Sidecars, sidecarView),
			Parent: d.Parent, Registry: d.Registry, Sidecar: sidecarPtr(d.Sidecar),
			HostURLs: d.HostURLs, HostRouter: d.HostRouter,
		}
		if c := d.Certificate; c != nil {
			out.Certificate = &certificate{Issuer: c.Issuer, DNSNames: orEmpty(c.DNSNames), Identity: c.Identity, NotAfter: timeView(c.NotAfter)}
		}

		return out, nil
	})

	s.addExplorers()
}

func (s *Server) addExplorers() {
	explore := func(in exploreIn) (core.ExploreRequest, error) {
		dir, err := s.dir(in.Dir)

		return core.ExploreRequest{Dir: dir, Service: in.Service}, err
	}

	add(s, readTool, true, &sdk.Tool{
		Name:        "postgres_tables",
		Description: "Tables in a running Postgres service, with estimated rows and size, read with psql inside its container.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (postgresOut, error) {
		req, err := explore(in)
		if err != nil {
			return postgresOut{}, err
		}

		res, err := s.eng.PostgresTables(ctx, req)
		if err != nil {
			return postgresOut{}, err
		}

		return postgresOut{Database: res.Database, Tables: mapSlice(res.Tables, func(t core.PostgresTable) postgresTable {
			return postgresTable{Schema: t.Schema, Name: t.Name, Rows: t.Rows, Bytes: t.Bytes}
		})}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "redis_keys",
		Description: "Keys in a running Redis service with their type and TTL (a bounded SCAN, read with redis-cli inside its container).",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (redisOut, error) {
		req, err := explore(in)
		if err != nil {
			return redisOut{}, err
		}

		res, err := s.eng.RedisKeys(ctx, req)
		if err != nil {
			return redisOut{}, err
		}

		return redisOut{Total: res.Total, Truncated: res.Truncated, Keys: mapSlice(res.Keys, func(k core.RedisKey) redisKey {
			return redisKey{Key: k.Key, Type: k.Type, TTL: k.TTL}
		})}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "consul_catalog",
		Description: "Services registered in a running Consul service with their health check counts, and its nodes, read from the Consul HTTPS API inside its container.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (consulOut, error) {
		req, err := explore(in)
		if err != nil {
			return consulOut{}, err
		}

		res, err := s.eng.ConsulCatalog(ctx, req)
		if err != nil {
			return consulOut{}, err
		}

		return consulOut{
			Services: mapSlice(res.Services, func(c core.ConsulService) consulService {
				return consulService{Name: c.Name, Tags: orEmpty(c.Tags), Instances: c.Instances, Passing: c.Passing, Warning: c.Warning, Critical: c.Critical}
			}),
			Nodes: mapSlice(res.Nodes, func(n core.ConsulNode) consulNode { return consulNode{Name: n.Name, Address: n.Address} }),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "kafka_topics",
		Description: "Topics in a running Kafka service (partitions, replication, message counts) and consumer groups (members, topics, lag), read with Kafka's own CLI tools inside its container. Takes several seconds.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (kafkaOut, error) {
		req, err := explore(in)
		if err != nil {
			return kafkaOut{}, err
		}

		res, err := s.eng.KafkaTopics(ctx, req)
		if err != nil {
			return kafkaOut{}, err
		}

		return kafkaOut{
			Topics: mapSlice(res.Topics, func(t core.KafkaTopic) kafkaTopic {
				return kafkaTopic{Name: t.Name, Partitions: t.Partitions, ReplicationFactor: t.ReplicationFactor, Messages: t.Messages, Internal: t.Internal}
			}),
			Groups: mapSlice(res.Groups, func(g core.KafkaGroup) kafkaGroup {
				return kafkaGroup{Name: g.Name, Topics: orEmpty(g.Topics), Members: g.Members, Lag: g.Lag}
			}),
		}, nil
	})

	sidecarReq := func(in sidecarIn) (core.ExploreRequest, error) {
		dir, err := s.dir(in.Dir)

		return core.ExploreRequest{Dir: dir, Service: in.Service}, err
	}

	add(s, readTool, true, &sdk.Tool{
		Name:        "envoy_clusters",
		Description: "What a running Envoy sees, an app's sidecar or an infra envoy proxy: its listeners, and each upstream cluster with its hosts (address, the replica or service at that address, health flags, outlier ejection, request, error and connection counts); an infra proxy's clusters carry the upstream's TLS mode. Read from Envoy's admin API inside the container. Use it to see which replica got traffic and why a host is failing.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in sidecarIn) (envoyOut, error) {
		req, err := sidecarReq(in)
		if err != nil {
			return envoyOut{}, err
		}

		res, err := s.eng.EnvoyClusters(ctx, req)
		if err != nil {
			return envoyOut{}, err
		}

		return envoyOut{
			Sidecar: res.Sidecar, App: res.App, Proxy: res.Proxy,
			Listeners: mapSlice(res.Listeners, func(l core.EnvoyListener) envoyListener { return envoyListener{Name: l.Name, Address: l.Address} }),
			Clusters: mapSlice(res.Clusters, func(c core.EnvoyCluster) envoyCluster {
				return envoyCluster{
					Name: c.Name, Role: c.Role, Target: c.Target, Mode: c.Mode, Retries: c.Retries,
					EjectionsActive: c.EjectionsActive, EjectionsEnforced: c.EjectionsEnforced, EjectionsOverflow: c.EjectionsOverflow,
					Hosts: mapSlice(c.Hosts, func(h core.EnvoyHost) envoyHost {
						return envoyHost{
							Address: h.Address, Hostname: h.Hostname, Instance: h.Instance, Healthy: h.Healthy, Flags: orEmpty(h.Flags), Ejected: h.Ejected,
							Requests: h.Requests, Successes: h.Successes, Errors: h.Errors, Timeouts: h.Timeouts, ConnectFailures: h.ConnectFailures,
							ActiveConnections: h.ActiveConnections, ActiveRequests: h.ActiveRequests,
						}
					}),
				}
			}),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "pgbouncer_pools",
		Description: "Pools of a running PgBouncer, an app's sidecar or an infra pgbouncer shared by apps (SHOW POOLS and SHOW STATS through psql inside it): client connections active and waiting, server connections to Postgres, transactions, queries and wait times.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in sidecarIn) (pgbouncerOut, error) {
		req, err := sidecarReq(in)
		if err != nil {
			return pgbouncerOut{}, err
		}

		res, err := s.eng.PgBouncerPools(ctx, req)
		if err != nil {
			return pgbouncerOut{}, err
		}

		return pgbouncerOut{
			Sidecar: res.Sidecar, App: res.App, Proxy: res.Proxy, Service: res.Service, PoolMode: res.PoolMode, PoolSize: res.PoolSize, Port: res.Port,
			Pools: mapSlice(res.Pools, func(p core.PgBouncerPool) pgbouncerPool {
				return pgbouncerPool{
					Database: p.Database, User: p.User, PoolMode: p.PoolMode,
					ClientsActive: p.ClientsActive, ClientsWaiting: p.ClientsWaiting,
					ServersActive: p.ServersActive, ServersIdle: p.ServersIdle, ServersUsed: p.ServersUsed, ServersLogin: p.ServersLogin,
					MaxWaitMicros: p.MaxWaitMicros, Transactions: p.Transactions, Queries: p.Queries,
					BytesReceived: p.BytesReceived, BytesSent: p.BytesSent, AvgQueryMicros: p.AvgQueryMicros, AvgWaitMicros: p.AvgWaitMicros,
				}
			}),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "s3_buckets",
		Description: "Buckets in a running S3 compatible service with object counts and sizes, read with signed S3 API requests inside its container.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (s3Out, error) {
		req, err := explore(in)
		if err != nil {
			return s3Out{}, err
		}

		res, err := s.eng.S3Buckets(ctx, req)
		if err != nil {
			return s3Out{}, err
		}

		return s3Out{Buckets: mapSlice(res.Buckets, func(b core.S3Bucket) s3Bucket {
			return s3Bucket{Name: b.Name, Objects: b.Objects, Bytes: b.Bytes, Truncated: b.Truncated}
		})}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "otel_telemetry",
		Description: "Live telemetry inside a running otel (LGTM) service: enabled signals, recent Tempo traces, Prometheus metric names and services, and recent Loki structured logs, read through loopback APIs inside the container.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (otelOut, error) {
		req, err := explore(in)
		if err != nil {
			return otelOut{}, err
		}

		res, err := s.eng.OtelTelemetry(ctx, req)
		if err != nil {
			return otelOut{}, err
		}

		return otelOut{
			Signals:        orEmpty(res.Signals),
			LogsEnabled:    res.LogsEnabled,
			MetricsEnabled: res.MetricsEnabled,
			TracesEnabled:  res.TracesEnabled,
			TraceServices:  orEmpty(res.TraceServices),
			Traces: mapSlice(res.Traces, func(t core.OtelTrace) otelTrace {
				return otelTrace{TraceID: t.TraceID, RootService: t.RootService, RootName: t.RootName, DurationMS: t.DurationMS, Spans: t.Spans}
			}),
			MetricServices: orEmpty(res.MetricServices),
			Metrics:        orEmpty(res.Metrics),
			MetricsMore:    res.MetricsMore,
			LogServices:    orEmpty(res.LogServices),
			Logs: mapSlice(res.Logs, func(l core.OtelLog) otelLog {
				return otelLog{Timestamp: l.Timestamp, Service: l.Service, Severity: l.Severity, TraceID: l.TraceID, Body: l.Body}
			}),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "opensearch_indices",
		Description: "Cluster health, version, shard counts, and user indices (with doc counts, storage sizes, and aliases) of a running OpenSearch service.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (opensearchOut, error) {
		req, err := explore(in)
		if err != nil {
			return opensearchOut{}, err
		}

		res, err := s.eng.OpenSearchIndices(ctx, req)
		if err != nil {
			return opensearchOut{}, err
		}

		return opensearchOut{
			ClusterName:      res.ClusterName,
			Status:           res.Status,
			Version:          res.Version,
			ActiveShards:     res.ActiveShards,
			UnassignedShards: res.UnassignedShards,
			Indices: mapSlice(res.Indices, func(idx core.OpenSearchIndex) opensearchIndex {
				return opensearchIndex{
					Name:      idx.Name,
					Health:    idx.Health,
					Status:    idx.Status,
					Primaries: idx.Primaries,
					Replicas:  idx.Replicas,
					Docs:      idx.Docs,
					Deleted:   idx.Deleted,
					Bytes:     idx.Bytes,
					Aliases:   orEmpty(idx.Aliases),
				}
			}),
		}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "nats_overview",
		Description: "Live NATS server stats (version, connections, subscriptions, traffic) and JetStream streams, KV buckets, and consumers of a running NATS service.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in exploreIn) (natsOut, error) {
		req, err := explore(in)
		if err != nil {
			return natsOut{}, err
		}

		res, err := s.eng.NatsOverview(ctx, req)
		if err != nil {
			return natsOut{}, err
		}

		return natsOut{
			Version:       res.Version,
			Connections:   res.Connections,
			Subscriptions: res.Subscriptions,
			InMsgs:        res.InMsgs,
			OutMsgs:       res.OutMsgs,
			InBytes:       res.InBytes,
			OutBytes:      res.OutBytes,
			JetStream:     res.JetStream,
			MemoryBytes:   res.MemoryBytes,
			StorageBytes:  res.StorageBytes,
			Streams: mapSlice(res.Streams, func(st core.NatsStream) natsStream {
				return natsStream{
					Name:      st.Name,
					Kind:      st.Kind,
					Subjects:  orEmpty(st.Subjects),
					Retention: st.Retention,
					Messages:  st.Messages,
					Bytes:     st.Bytes,
					Consumers: mapSlice(st.Consumers, func(c core.NatsConsumer) natsConsumer {
						return natsConsumer{
							Name:       c.Name,
							Pending:    c.Pending,
							AckPending: c.AckPending,
							Delivered:  c.Delivered,
						}
					}),
				}
			}),
		}, nil
	})

	s.addProxyExplorers(explore)
}

type natsConsumer struct {
	Name       string `json:"name"`
	Pending    uint64 `json:"pending"`
	AckPending int    `json:"ack_pending"`
	Delivered  uint64 `json:"delivered"`
}

type natsStream struct {
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Subjects  []string       `json:"subjects"`
	Retention string         `json:"retention"`
	Messages  uint64         `json:"messages"`
	Bytes     uint64         `json:"bytes"`
	Consumers []natsConsumer `json:"consumers"`
}

type natsOut struct {
	Version       string       `json:"version"`
	Connections   int          `json:"connections"`
	Subscriptions int          `json:"subscriptions"`
	InMsgs        int64        `json:"in_msgs"`
	OutMsgs       int64        `json:"out_msgs"`
	InBytes       int64        `json:"in_bytes"`
	OutBytes      int64        `json:"out_bytes"`
	JetStream     bool         `json:"jetstream"`
	MemoryBytes   int64        `json:"memory_bytes"`
	StorageBytes  int64        `json:"storage_bytes"`
	Streams       []natsStream `json:"streams"`
}

type opensearchIndex struct {
	Name      string   `json:"name"`
	Health    string   `json:"health"`
	Status    string   `json:"status"`
	Primaries int      `json:"primaries"`
	Replicas  int      `json:"replicas"`
	Docs      int64    `json:"docs"`
	Deleted   int64    `json:"deleted"`
	Bytes     int64    `json:"bytes"`
	Aliases   []string `json:"aliases"`
}

type opensearchOut struct {
	ClusterName      string            `json:"cluster_name"`
	Status           string            `json:"status"`
	Version          string            `json:"version"`
	ActiveShards     int               `json:"active_shards"`
	UnassignedShards int               `json:"unassigned_shards"`
	Indices          []opensearchIndex `json:"indices"`
}

type otelTrace struct {
	TraceID     string `json:"trace_id"`
	RootService string `json:"root_service"`
	RootName    string `json:"root_name"`
	DurationMS  int64  `json:"duration_ms"`
	Spans       int    `json:"spans"`
}

type otelLog struct {
	Timestamp string `json:"timestamp"`
	Service   string `json:"service"`
	Severity  string `json:"severity,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	Body      string `json:"body"`
}

type otelOut struct {
	Signals        []string    `json:"signals"`
	LogsEnabled    bool        `json:"logs_enabled"`
	MetricsEnabled bool        `json:"metrics_enabled"`
	TracesEnabled  bool        `json:"traces_enabled"`
	TraceServices  []string    `json:"trace_services"`
	Traces         []otelTrace `json:"traces"`
	MetricServices []string    `json:"metric_services"`
	Metrics        []string    `json:"metrics"`
	MetricsMore    bool        `json:"metrics_more,omitempty"`
	LogServices    []string    `json:"log_services"`
	Logs           []otelLog   `json:"logs"`
}
