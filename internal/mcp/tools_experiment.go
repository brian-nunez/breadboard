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
	Env         []envGroup   `json:"env" jsonschema:"secret values are empty; call creds for them"`
	Certificate *certificate `json:"certificate,omitempty" jsonschema:"absent before the first up"`
	Path        string       `json:"path,omitempty"`
	Command     []string     `json:"command,omitempty"`
	Parent      string       `json:"parent,omitempty"`
	Registry    string       `json:"registry,omitempty"`
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
		return statusOut{Experiment: res.Experiment, Domain: res.Domain, Services: mapSlice(res.Services, statusView), Warnings: orEmpty(res.Warnings)}, nil
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
			UsedBy: orEmpty(d.UsedBy),
			Env: mapSlice(d.Env, func(g core.EnvGroup) envGroup {
				return envGroup{Service: g.Service, Title: g.Title, Plaintext: g.Plaintext, Reason: g.Reason, Vars: mapSlice(g.Vars, envVarView)}
			}),
			Path: d.Path, Command: d.Command, Parent: d.Parent, Registry: d.Registry,
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
}
