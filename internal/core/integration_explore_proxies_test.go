//go:build integration

package core

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestIntegrationProxyExplorers runs each proxy type in front of web (3
// replicas, re-encrypt), api (terminate) and payments (passthrough), plus
// a pool in front of Postgres shared by two apps, sends traffic, and
// checks that every proxy explorer reads real data: which replicas are up
// and, where the proxy counts it, how many requests each got.
func TestIntegrationProxyExplorers(t *testing.T) {
	upstreams := `    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
`
	eng, dir, name := sidecarLab(t, "proxy-x", `services:
  edge:
    type: haproxy
`+upstreams+`  gw:
    type: envoy
`+upstreams+`  router:
    type: traefik
`+upstreams+`  front:
    type: nginx
`+upstreams+`  postgres:
    type: postgres
  pool:
    type: pgbouncer
    upstreams: [postgres]
apps:
  web:
    path: ./probe
    command: [probe, servetls, ":8443"]
    replicas: 3
  api:
    path: ./probe
    command: [probe, serve]
    replicas: 2
  payments:
    path: ./probe
    command: [probe, servetls, ":8443"]
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge, gw, router, front]
  orders:
    path: ./pg
    command: [sh, -c, "trap 'exit 0' TERM; while :; do sleep 1; done"]
    uses: [pool]
  billing:
    path: ./pg
    command: [sh, -c, "trap 'exit 0' TERM; while :; do sleep 1; done"]
    uses: [pool]
`, map[string]map[string]string{"pg": {"Dockerfile": "FROM postgres:18.7-alpine3.24 AS dev\n"}})
	ctx := context.Background()
	req := func(service string) ExploreRequest { return ExploreRequest{Dir: dir, Service: service} }
	// traffic sends 30 requests to web through a proxy once every replica
	// answers through it, and one each to api and payments.
	traffic := func(t *testing.T, env string) {
		t.Helper()
		eventually(t, time.Minute, "every replica to answer through "+env, func() bool {
			out, err := execIn(t, name, "client", `probe hits "$`+env+`_URL/web/" 30`)

			return err == nil && strings.Contains(out, "web-1") && strings.Contains(out, "web-2") && strings.Contains(out, "web-3")
		})

		if out, err := execIn(t, name, "client", `probe get "$`+env+`_URL/api/" && probe peer "$`+env+`_PASSTHROUGH_ADDR" payments.bb.internal`); err != nil {
			t.Fatalf("api and payments through %s: %v\n%s", env, err, out)
		}
	}

	t.Run("haproxy", func(t *testing.T) {
		traffic(t, "EDGE")

		res, err := eng.HAProxyStats(ctx, req("edge"))
		if err != nil {
			t.Fatal(err)
		}

		var web, api, pay *HAProxyBackend

		for i, b := range res.Backends {
			switch b.Upstream.Route {
			case "web":
				web = &res.Backends[i]
			case "api":
				api = &res.Backends[i]
			case "payments":
				pay = &res.Backends[i]
			}
		}

		if web == nil || api == nil || pay == nil {
			t.Fatalf("backends = %+v", res.Backends)
		}

		var instances []string
		for _, s := range web.Servers {
			instances = append(instances, s.Instance)
			if !s.Up || s.TotalSessions == 0 || s.CheckStatus != "L6OK" {
				t.Errorf("web server %+v: want up, checked over TLS, with traffic", s)
			}
		}

		slices.Sort(instances)

		if !slices.Equal(instances, []string{"web-1", "web-2", "web-3"}) || web.Upstream.Mode != "on" {
			t.Errorf("web backend: instances %v, upstream %+v", instances, web.Upstream)
		}

		if api.Upstream.Mode != "off" || len(api.Servers) != 2 || api.TotalSessions == 0 || !strings.HasPrefix(api.Servers[0].Address, "10.213.") {
			t.Errorf("api backend = %+v", api)
		}

		if pay.Mode != "tcp" || pay.Upstream.Mode != "passthrough" || pay.Servers[0].Instance != "payments" || pay.TotalSessions == 0 {
			t.Errorf("payments backend = %+v", pay)
		}

		t.Logf("web servers: %+v", web.Servers)
	})

	t.Run("traefik", func(t *testing.T) {
		traffic(t, "ROUTER")

		res, err := eng.TraefikRoutes(ctx, req("router"))
		if err != nil {
			t.Fatal(err)
		}

		if len(res.Routers) != 4 {
			t.Errorf("routers = %+v", res.Routers)
		}

		for _, s := range res.Services {
			var states []string
			for _, sv := range s.Servers {
				states = append(states, sv.Instance+"="+sv.Status)
			}

			switch s.Upstream.Route {
			case "web":
				if !slices.Equal(states, []string{"web-1=UP", "web-2=UP", "web-3=UP"}) || s.Upstream.Mode != "on" {
					t.Errorf("web service: %v %+v", states, s.Upstream)
				}
			case "api":
				if !slices.Equal(states, []string{"api-1=UP", "api-2=UP"}) || s.Upstream.Mode != "off" {
					t.Errorf("api service: %v", states)
				}
			case "payments":
				if !slices.Equal(states, []string{"payments=UP"}) || s.Protocol != "tcp" {
					t.Errorf("payments service: %v", states)
				}
			default:
				t.Errorf("service %s matches no upstream", s.Name)
			}
		}
	})

	t.Run("nginx", func(t *testing.T) {
		before, err := eng.NginxStatus(ctx, req("front"))
		if err != nil {
			t.Fatal(err)
		}

		traffic(t, "FRONT")

		res, err := eng.NginxStatus(ctx, req("front"))
		if err != nil {
			t.Fatal(err)
		}

		if res.Requests < before.Requests+31 || res.Accepts == 0 || res.Active < 1 {
			t.Errorf("stub_status before %+v after %+v", before, res)
		}

		if len(res.Upstreams) != 3 || len(res.Upstreams[0].Backends) != 3 {
			t.Fatalf("upstreams = %+v", res.Upstreams)
		}

		for _, u := range res.Upstreams {
			for _, b := range u.Backends {
				if b.Address == "" {
					t.Errorf("%s: %s does not resolve inside NGINX", u.Route, b.Name)
				}
			}
		}

		if b := res.Upstreams[1].Backends[0]; !strings.HasPrefix(b.Address, "10.213.") || b.Instance != "api-1" {
			t.Errorf("terminate backend = %+v", b)
		}
	})

	t.Run("envoy", func(t *testing.T) {
		eventually(t, time.Minute, "Envoy to know all three replicas", func() bool {
			res, err := eng.EnvoyClusters(ctx, req("gw"))
			if err != nil {
				return false
			}

			for _, c := range res.Clusters {
				if c.Name == "web" {
					return len(c.Hosts) == 3
				}
			}

			return false
		})
		traffic(t, "GW")

		res, err := eng.EnvoyClusters(ctx, req("gw"))
		if err != nil {
			t.Fatal(err)
		}

		if res.Proxy != "gw" || res.Sidecar != "" || len(res.Listeners) != 2 {
			t.Errorf("envoy = %+v", res)
		}

		for _, c := range res.Clusters {
			var (
				instances []string
				total     int64
			)

			for _, h := range c.Hosts {
				instances = append(instances, h.Instance)

				total += h.Requests
				if !h.Healthy {
					t.Errorf("%s host %+v unhealthy", c.Name, h)
				}
			}

			switch c.Name {
			case "web":
				if c.Role != "upstream" || c.Mode != "on" || !slices.Equal(instances, []string{"web-1", "web-2", "web-3"}) || total < 30 {
					t.Errorf("web cluster: %v with %d requests (%+v)", instances, total, c)
				}
			case "api":
				if c.Mode != "off" || !slices.Equal(instances, []string{"api-1", "api-2"}) {
					t.Errorf("api cluster: %v (%+v)", instances, c)
				}
			case "tls_payments":
				if c.Mode != "passthrough" || !slices.Equal(instances, []string{"payments"}) {
					t.Errorf("payments cluster: %v (%+v)", instances, c)
				}
			}
		}
	})

	t.Run("pgbouncer", func(t *testing.T) {
		for _, app := range []string{"orders", "billing"} {
			if out, err := execIn(t, name, app, `psql "$DATABASE_URL" -tAc "select 1"`); err != nil {
				t.Fatalf("%s through the pool: %v\n%s", app, err, out)
			}
		}

		res, err := eng.PgBouncerPools(ctx, req("pool"))
		if err != nil {
			t.Fatal(err)
		}

		if res.Proxy != "pool" || res.Service != "postgres" || res.Port != 6432 || res.PoolMode != "transaction" || res.PoolSize != 10 || len(res.Pools) != 1 {
			t.Fatalf("pools = %+v", res)
		}

		if p := res.Pools[0]; p.Database != "app" || p.Queries < 2 {
			t.Errorf("pool = %+v", p)
		}
	})
}
