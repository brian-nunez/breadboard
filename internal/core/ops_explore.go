package core

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
)

// Explorers read what a running service holds, for the web UI. Each runs the
// service's own command-line tool inside its container (ADR-0012) and parses
// the output; none of them changes anything.

// ExploreRequest names the service to explore.
type ExploreRequest struct {
	Dir     string
	Service string
}

// explore resolves the service, checks its catalog type, and returns a
// function that runs a command in its container.
func (e *Engine) explore(ctx context.Context, req ExploreRequest, typ string) (*model.Service, func(...string) ([]byte, error), error) {
	if err := e.rt.Available(ctx); err != nil {
		return nil, nil, err
	}
	p, err := e.open(req.Dir)
	if err != nil {
		return nil, nil, err
	}
	st, err := e.settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	m, err := model.Resolve(p.spec, st.Domain)
	if err != nil {
		return nil, nil, err
	}
	svc, ok := m.Service(req.Service)
	if !ok || svc.Kind != model.Infra || svc.Entry.ID != typ {
		return nil, nil, fmt.Errorf("no %s service named %q: %w", typ, req.Service, ErrNotFound)
	}
	run := func(cmd ...string) ([]byte, error) {
		return e.rt.Exec(ctx, m.Name, svc.Name, cmd)
	}
	return svc, run, nil
}

// --- Postgres ---------------------------------------------------------------

// PostgresTables lists the tables of the app database.
type PostgresTables struct {
	Database string
	Tables   []PostgresTable
}

// PostgresTable is one table with its exact row count and total size.
type PostgresTable struct {
	Schema string
	Name   string
	Rows   int64
	Bytes  int64
}

// postgresTablesSQL counts every user table exactly (fine at lab sizes).
const postgresTablesSQL = `select table_schema, table_name,
  (xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', table_schema, table_name), false, true, '')))[1]::text,
  pg_total_relation_size(format('%I.%I', table_schema, table_name)::regclass)
from information_schema.tables
where table_type = 'BASE TABLE' and table_schema not in ('pg_catalog', 'information_schema')
order by table_schema, table_name`

// PostgresTables lists tables in the service's app database, connecting as
// the superuser over the container's local socket.
func (e *Engine) PostgresTables(ctx context.Context, req ExploreRequest) (*PostgresTables, error) {
	svc, run, err := e.explore(ctx, req, "postgres")
	if err != nil {
		return nil, err
	}
	db := svc.Config["database"]
	out, err := run("psql", "-U", "postgres", "-d", db, "-At", "-F", "\t", "-c", postgresTablesSQL)
	if err != nil {
		return nil, err
	}
	tables, err := parsePostgresTables(string(out))
	if err != nil {
		return nil, err
	}
	return &PostgresTables{Database: db, Tables: tables}, nil
}

func parsePostgresTables(out string) ([]PostgresTable, error) {
	var tables []PostgresTable
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			return nil, fmt.Errorf("psql: unexpected line %q", line)
		}
		rows, _ := strconv.ParseInt(f[2], 10, 64)
		size, _ := strconv.ParseInt(f[3], 10, 64)
		tables = append(tables, PostgresTable{Schema: f[0], Name: f[1], Rows: rows, Bytes: size})
	}
	return tables, nil
}

// --- Redis ------------------------------------------------------------------

// RedisKeyLimit caps how many keys the Redis explorer returns.
const RedisKeyLimit = 200

// RedisKeys is a sample of the keyspace.
type RedisKeys struct {
	Total     int64 // DBSIZE
	Keys      []RedisKey
	Truncated bool // more keys exist than were returned
}

// RedisKey is one key with its type and time to live in seconds (-1: none).
type RedisKey struct {
	Key  string
	Type string
	TTL  int64
}

// redisScan walks the keyspace with SCAN inside one script, so a single
// round trip returns DBSIZE and up to the limit of keys with type and TTL.
const redisScan = `local out = {redis.call('DBSIZE')}
local cursor = '0'
repeat
  local r = redis.call('SCAN', cursor, 'COUNT', 100)
  cursor = r[1]
  for _, k in ipairs(r[2]) do
    table.insert(out, k)
    table.insert(out, redis.call('TYPE', k).ok)
    table.insert(out, redis.call('TTL', k))
    if #out >= 1 + 3 * tonumber(ARGV[1]) then return out end
  end
until cursor == '0'
return out`

// RedisKeys samples the service's keys over TLS with the mounted CA.
func (e *Engine) RedisKeys(ctx context.Context, req ExploreRequest) (*RedisKeys, error) {
	svc, run, err := e.explore(ctx, req, "redis")
	if err != nil {
		return nil, err
	}
	port := svc.Entry.Listeners[0].Port
	script := fmt.Sprintf(`redis-cli --tls --cacert %s -h %s -p %d --no-auth-warning -a "$REDIS_PASSWORD" --json EVAL "$0" 0 %d`, catalog.CAFile, svc.FQDN, port, RedisKeyLimit)
	out, err := run("sh", "-c", script, redisScan)
	if err != nil {
		return nil, err
	}
	return parseRedisKeys(out)
}

func parseRedisKeys(out []byte) (*RedisKeys, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("redis-cli: unexpected output %q", strings.TrimSpace(string(out)))
	}
	res := &RedisKeys{}
	_ = json.Unmarshal(raw[0], &res.Total)
	for i := 1; i+2 < len(raw); i += 3 {
		var k RedisKey
		_ = json.Unmarshal(raw[i], &k.Key)
		_ = json.Unmarshal(raw[i+1], &k.Type)
		_ = json.Unmarshal(raw[i+2], &k.TTL)
		res.Keys = append(res.Keys, k)
	}
	slices.SortFunc(res.Keys, func(a, b RedisKey) int { return strings.Compare(a.Key, b.Key) })
	res.Truncated = int64(len(res.Keys)) < res.Total
	return res, nil
}

// --- Consul -----------------------------------------------------------------

// ConsulCatalog is what the Consul server knows.
type ConsulCatalog struct {
	Services []ConsulService
	Nodes    []ConsulNode
}

// ConsulService is one registered service and the health of its instances.
type ConsulService struct {
	Name      string
	Tags      []string
	Instances int // instances with at least one check
	Passing   int
	Warning   int
	Critical  int
}

// ConsulNode is one agent in the cluster.
type ConsulNode struct {
	Name    string
	Address string
}

// ConsulCatalog reads the catalog through the server's HTTPS API, from
// inside its container, verifying the certificate with the mounted CA.
func (e *Engine) ConsulCatalog(ctx context.Context, req ExploreRequest) (*ConsulCatalog, error) {
	svc, run, err := e.explore(ctx, req, "consul")
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("https://%s:%d/v1", svc.FQDN, svc.Entry.Listeners[0].Port)
	get := func(path string) ([]byte, error) {
		return run("curl", "-sf", "--cacert", catalog.CAFile, base+path)
	}
	services, err := get("/catalog/services")
	if err != nil {
		return nil, err
	}
	checks, err := get("/health/state/any")
	if err != nil {
		return nil, err
	}
	nodes, err := get("/catalog/nodes")
	if err != nil {
		return nil, err
	}
	return parseConsulCatalog(services, checks, nodes)
}

func parseConsulCatalog(services, checks, nodes []byte) (*ConsulCatalog, error) {
	var names map[string][]string
	if err := json.Unmarshal(services, &names); err != nil {
		return nil, fmt.Errorf("consul services: %w", err)
	}
	var cs []struct {
		ServiceName string
		ServiceID   string
		Status      string
	}
	if err := json.Unmarshal(checks, &cs); err != nil {
		return nil, fmt.Errorf("consul checks: %w", err)
	}
	var ns []struct {
		Node    string
		Address string
	}
	if err := json.Unmarshal(nodes, &ns); err != nil {
		return nil, fmt.Errorf("consul nodes: %w", err)
	}
	// An instance's state is its worst check.
	type key struct{ service, id string }
	worst := map[key]string{}
	rank := map[string]int{"passing": 0, "warning": 1, "critical": 2}
	for _, c := range cs {
		if c.ServiceName == "" {
			continue // a node check
		}
		k := key{c.ServiceName, c.ServiceID}
		if old, ok := worst[k]; !ok || rank[c.Status] > rank[old] {
			worst[k] = c.Status
		}
	}
	res := &ConsulCatalog{}
	for name, tags := range names {
		s := ConsulService{Name: name, Tags: tags}
		for k, state := range worst {
			if k.service != name {
				continue
			}
			s.Instances++
			switch state {
			case "passing":
				s.Passing++
			case "warning":
				s.Warning++
			default:
				s.Critical++
			}
		}
		res.Services = append(res.Services, s)
	}
	slices.SortFunc(res.Services, func(a, b ConsulService) int { return strings.Compare(a.Name, b.Name) })
	for _, n := range ns {
		res.Nodes = append(res.Nodes, ConsulNode{Name: n.Node, Address: n.Address})
	}
	slices.SortFunc(res.Nodes, func(a, b ConsulNode) int { return strings.Compare(a.Name, b.Name) })
	return res, nil
}
