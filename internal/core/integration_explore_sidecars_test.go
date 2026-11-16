//go:build integration

package core

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestIntegrationEnvoyExplorer: the Envoy explorer reads a real sidecar's
// admin API and shows which replica each request went to.
func TestIntegrationEnvoyExplorer(t *testing.T) {
	eng, dir, name := sidecarLab(t, "envoy-x", `apps:
  web:
    path: ./probe
    command: [probe, serve]
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  caller:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
`, nil)
	ctx := context.Background()
	// Envoy resolves web.bb.internal every few seconds; wait until it
	// knows every replica, then send traffic.
	eventually(t, time.Minute, "Envoy to know all three replicas", func() bool {
		res, err := eng.EnvoyClusters(ctx, ExploreRequest{Dir: dir, Service: "caller"})
		return err == nil && len(res.Clusters) == 1 && len(res.Clusters[0].Hosts) == 3
	})
	if out, err := execIn(t, name, "caller", `probe hits "$WEB_URL" 30`); err != nil {
		t.Fatalf("hits: %v\n%s", err, out)
	}
	res, err := eng.EnvoyClusters(ctx, ExploreRequest{Dir: dir, Service: "caller"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sidecar != "caller-envoy" || len(res.Listeners) != 1 || res.Listeners[0].Address != "127.0.0.1:9001" {
		t.Errorf("caller envoy = %+v", res)
	}
	c := res.Clusters[0]
	if c.Name != "outbound_web" || c.Role != "outbound" || c.Target != "web" {
		t.Fatalf("cluster = %+v", c)
	}
	var instances []string
	var total int64
	for _, h := range c.Hosts {
		instances = append(instances, h.Instance)
		total += h.Requests
		if !h.Healthy || h.Requests == 0 {
			t.Errorf("host %+v: want healthy with traffic", h)
		}
	}
	if !slices.Equal(instances, []string{"web-1", "web-2", "web-3"}) || total < 30 {
		t.Errorf("hosts %v with %d requests", instances, total)
	}
	t.Logf("per replica: %+v", c.Hosts)

	in, err := eng.EnvoyClusters(ctx, ExploreRequest{Dir: dir, Service: "web-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Clusters) != 1 || in.Clusters[0].Role != "inbound" || in.Clusters[0].Hosts[0].Address != "127.0.0.1:8080" || in.Clusters[0].Hosts[0].Instance != "web-2" {
		t.Errorf("web-2 envoy = %+v", in)
	}
}

// TestIntegrationPgBouncerExplorer: the PgBouncer explorer reads a real
// sidecar's pools and counts the app's queries.
func TestIntegrationPgBouncerExplorer(t *testing.T) {
	eng, dir, name := sidecarLab(t, "pgb-x", `services:
  postgres:
    type: postgres
apps:
  db:
    path: ./pg
    command: [sh, -c, "trap 'exit 0' TERM; while :; do sleep 1; done"]
    uses: [postgres]
    sidecars:
      - type: pgbouncer
        service: postgres
        pool_mode: session
        pool_size: 4
`, map[string]map[string]string{"pg": {"Dockerfile": "FROM postgres:18.7-alpine3.24 AS dev\n"}})
	for range 3 {
		if out, err := execIn(t, name, "db", `psql "$DATABASE_URL" -tAc "select 1"`); err != nil {
			t.Fatalf("query through PgBouncer: %v\n%s", err, out)
		}
	}
	res, err := eng.PgBouncerPools(context.Background(), ExploreRequest{Dir: dir, Service: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sidecar != "db-pgbouncer" || res.Service != "postgres" || res.PoolMode != "session" || res.PoolSize != 4 || len(res.Pools) != 1 {
		t.Fatalf("pools = %+v", res)
	}
	p := res.Pools[0]
	if p.Database != "app" || p.PoolMode != "session" || p.Queries < 3 || p.ServersIdle+p.ServersUsed+p.ServersActive < 1 {
		t.Errorf("app pool = %+v", p)
	}
	t.Logf("pool: %+v", p)
}
