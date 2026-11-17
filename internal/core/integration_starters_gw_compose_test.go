//go:build integration

package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIntegrationStarterGWBFFAggregation: one client call to the BFF
// returns the merged screen; both services logged that call's request ID;
// the BFF's fan-out took less than its two calls added up (concurrent);
// and the BFF answer is smaller than the two routed answers together.
func TestIntegrationStarterGWBFFAggregation(t *testing.T) {
	eng, dir, _ := sidecarStarterLab(t, "gw-bff", "gw/bff-aggregation")
	lastLines(t, eng, dir, "bff-client", "bff-api", "bff-profile", "bff-orders", "bff-edge")

	eventually(t, time.Minute, "Traefik to load the routers with every server up", func() bool {
		routes, err := eng.TraefikRoutes(context.Background(), ExploreRequest{Dir: dir, Service: "bff-edge"})
		if err != nil {
			t.Fatal(err)
		}
		loaded := map[string]string{}
		for _, r := range routes.Routers {
			loaded["router "+r.Name] = r.Status
		}
		for _, s := range routes.Services {
			up := 0
			for _, sv := range s.Servers {
				if sv.Status == "UP" {
					up++
				}
			}
			loaded["service "+s.Name] = fmt.Sprintf("%s %d up", s.Status, up)
		}
		for _, n := range []string{"bff", "profile", "orders"} {
			if loaded["router "+n+"@file"] != "enabled" || loaded["service "+n+"@file"] != "enabled 1 up" {
				return false
			}
		}
		t.Logf("Traefik loaded: %v", loaded)
		return true
	})

	bff := regexp.MustCompile(`backend for frontend \(request (home-\d+)\): 1 call  in\s+(\d+)ms,\s+(\d+) bytes -> (name="Alice Example" tier=gold recent_orders=3 open_orders=2)$`)
	routed := regexp.MustCompile(`gateway routing \(request (direct-\d+)\): 2 calls in\s+(\d+)ms,\s+(\d+) bytes -> (name="Alice Example" tier=gold recent_orders=3 open_orders=2)$`)
	var lines []string
	eventually(t, 2*time.Minute, "three rounds loaded both ways", func() bool {
		lines = logsOf(t, eng, dir, "bff-client")
		return len(matches(lines, bff)) >= 3 && len(matches(lines, routed)) >= 3
	})
	var b, r []string
	for _, l := range slices.Backward(lines) {
		if m := bff.FindStringSubmatch(l); m != nil && b == nil {
			b = m
		}
		if m := routed.FindStringSubmatch(l); m != nil && r == nil {
			r = m
		}
	}
	t.Logf("BFF: request %s, %sms, %s bytes: %s", b[1], b[2], b[3], b[4])
	t.Logf("gateway routing: request %s, %sms, %s bytes: %s", r[1], r[2], r[3], r[4])
	if atoi(t, b[3])*2 >= atoi(t, r[3]) {
		t.Errorf("the BFF sent %s bytes, the two routed calls %s: want under half", b[3], r[3])
	}
	if atoi(t, b[2]) >= atoi(t, r[2]) {
		t.Errorf("the BFF screen took %sms, the routed one %sms: want faster", b[2], r[2])
	}

	// Both services logged the BFF call's request ID: one client call, two
	// service calls.
	id := b[1]
	for _, s := range []string{"bff-profile", "bff-orders"} {
		if !slices.ContainsFunc(logsOf(t, eng, dir, s), func(l string) bool { return strings.Contains(l, "(request "+id+")") }) {
			t.Errorf("%s logged no call for request %s", s, id)
		}
	}
	// The BFF waited for the slower call, not for both in turn.
	fan := regexp.MustCompile(`home for alice \(request ` + regexp.QuoteMeta(id) + `\): fanned out to profile \((\d+)ms\) and orders \((\d+)ms\) concurrently, answered in (\d+)ms$`)
	var f []string
	for _, l := range logsOf(t, eng, dir, "bff-api") {
		if f = fan.FindStringSubmatch(l); f != nil {
			t.Logf("bff-api: %s", f[0])
			break
		}
	}
	if f == nil {
		t.Fatalf("bff-api logged no fan-out for request %s", id)
	}
	if p, o, total := atoi(t, f[1]), atoi(t, f[2]), atoi(t, f[3]); total >= p+o {
		t.Errorf("the fan-out took %dms, the calls %dms and %dms: not concurrent", total, p, o)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegrationStarterGWGRPCRouting: one Envoy listener reaches two gRPC
// services by name over HTTP/2 (both catalog replicas over the client's
// single connection); a service no route names is UNIMPLEMENTED; the slow
// method ends DEADLINE_EXCEEDED at the route's 500 ms, not the client's 2s
// deadline; Envoy counts gRPC calls per method.
func TestIntegrationStarterGWGRPCRouting(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "gw-grpc", "gw/grpc-routing")
	lastLines(t, eng, dir, "grpc-client", "grpc-catalog-1", "grpc-catalog-2", "grpc-inventory", "grpc-edge")
	envoyConfigValid(t, name, "grpc-edge")

	getItem := regexp.MustCompile(`(catalog\.v1\.Catalog/GetItem x4\s+-> OK x4, served by grpc-catalog-1=\d grpc-catalog-2=\d)$`)
	stock := regexp.MustCompile(`(inventory\.v1\.Inventory/CheckStock\s+-> OK after \d+ms from grpc-inventory: .*in_stock:true.*)$`)
	search := regexp.MustCompile(`(catalog\.v1\.Catalog/Search\s+-> DeadlineExceeded after (\d+)ms: downstream duration timeout)$`)
	unknown := regexp.MustCompile(`(billing\.v1\.Billing/Charge\s+-> Unimplemented after \d+ms)`)
	var lines []string
	eventually(t, 3*time.Minute, "a round with every call answered as expected", func() bool {
		lines = logsOf(t, eng, dir, "grpc-client")
		return !slices.ContainsFunc([]*regexp.Regexp{getItem, stock, search, unknown}, func(re *regexp.Regexp) bool { return len(matches(lines, re)) == 0 })
	})
	for _, re := range []*regexp.Regexp{getItem, stock, search, unknown} {
		t.Log(matches(lines, re)[0])
	}
	for _, l := range lines {
		if m := search.FindStringSubmatch(l); m != nil {
			if ms := atoi(t, m[2]); ms < 450 || ms > 1500 {
				t.Errorf("Search ended after %dms, want the route's 500ms: %s", ms, l)
			}
		}
		if strings.Contains(l, "catalog.v1.Catalog/Search") && strings.Contains(l, "-> OK") {
			t.Errorf("Search succeeded past its limit: %s", l)
		}
	}

	// The catalog saw the call cancelled when Envoy gave up.
	catalog := append(logsOf(t, eng, dir, "grpc-catalog-1"), logsOf(t, eng, dir, "grpc-catalog-2")...)
	for _, want := range []string{"GetItem SKU-100 (", `Search "widgets" started (`, `Search "widgets" abandoned after`} {
		i := slices.IndexFunc(catalog, func(l string) bool { return strings.Contains(l, want) })
		if i < 0 {
			t.Errorf("no catalog replica logged %q", want)
			continue
		}
		t.Logf("catalog: %s", catalog[i])
	}

	stats := envoyAdmin(t, name, "grpc-edge", "/stats?filter=grpc")
	t.Logf("grpc-edge stats:\n%s", stats)
	for _, s := range []string{
		"cluster.catalog.grpc.catalog.v1.Catalog.GetItem.success",
		"cluster.catalog.grpc.catalog.v1.Catalog.Search.failure",
		// Per gRPC status code: 4 is DEADLINE_EXCEEDED.
		"cluster.catalog.grpc.catalog.v1.Catalog.Search.4",
		"cluster.inventory.grpc.inventory.v1.Inventory.CheckStock.success",
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(s) + `: [1-9]`).MatchString(stats) {
			t.Errorf("grpc-edge counted no %s", s)
		}
	}
}
