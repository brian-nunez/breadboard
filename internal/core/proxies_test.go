package core

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/errs"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

const proxySpec = `version: 1
name: lab
services:
  edge:
    type: haproxy
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        tls: {mode: passthrough}
        port: 8443
apps:
  web:
    path: ./probe
    replicas: 3
  api:
    path: ./probe
    replicas: 2
  payments:
    path: ./probe
  client:
    path: ./probe
    uses: [edge]
`

// Status shows a proxy's upstream edges with their modes and warns about
// each terminate hop; apps behind it see who proxies to them.
func TestStatusShowsProxyUpstreams(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec(proxySpec)

	res, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}

	rows := rowsByName(res.Services)

	var got []string
	for _, e := range rows["edge"].Uses {
		got = append(got, e.To+"/"+e.Mode)
		if e.To == "api" && (e.TLS || e.Port != 8080 || !e.ToApp || e.Reason == "") {
			t.Errorf("terminate edge = %+v", e)
		}
	}

	if !slices.Equal(got, []string{"web/on", "api/off", "payments/passthrough"}) {
		t.Errorf("edge uses = %v", got)
	}

	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "edge -> api:8080") }) {
		t.Errorf("no plaintext warning for the terminate hop: %v", res.Warnings)
	}

	if c := rows["client"].Uses; len(c) != 1 || c[0].To != "edge" || c[0].Port != 8443 || !c[0].TLS {
		t.Errorf("client uses = %+v", c)
	}

	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "api"})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(d.UsedBy, "edge") {
		t.Errorf("api used by %v", d.UsedBy)
	}

	var plain string

	for _, g := range d.Env {
		for _, v := range g.Vars {
			if v.Name == "BB_PLAIN_LISTEN_ADDR" {
				plain = v.Value
			}
		}
	}

	if !strings.HasSuffix(plain, ":8080") {
		t.Errorf("api BB_PLAIN_LISTEN_ADDR = %q", plain)
	}
}

// A proxy's details show the passthrough listener and variable only when
// an upstream is passthrough.
func TestProxyDetailsShowLiveListeners(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want bool
	}{
		{proxySpec, true},
		{strings.Replace(proxySpec, "tls: {mode: passthrough}", "tls: {mode: on}", 1), false},
	} {
		h := newHarness(t)
		h.init()
		h.writeSpec(tc.spec)

		d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "edge"})
		if err != nil {
			t.Fatal(err)
		}

		listens := slices.ContainsFunc(d.Listeners, func(l Listener) bool { return l.Port == 9443 })

		var env bool
		for _, g := range d.Env {
			env = env || slices.ContainsFunc(g.Vars, func(v EnvVar) bool { return v.Name == "EDGE_PASSTHROUGH_ADDR" && v.Value == "edge.bb.internal:9443" })
		}

		if listens != tc.want || env != tc.want || !slices.ContainsFunc(d.Listeners, func(l Listener) bool { return l.Port == 8443 }) {
			t.Errorf("passthrough %v: listeners %+v env %+v", tc.want, d.Listeners, d.Env)
		}
	}
}

// A proxy's upstream cannot be removed out from under it.
func TestRemoveServiceRefusesAnUpstream(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec(proxySpec)

	_, err := h.eng.RemoveService(h.ctx(), RemoveServiceRequest{Dir: h.dir, Name: "api"})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "services.edge.upstreams[1]") {
		t.Fatalf("RemoveService(api) = %v", err)
	}
}

// An app segment created before its ip_range existed is recreated (its
// containers stopped, volumes kept); one that has it is left alone.
func TestAppSegmentWithoutIPRangeIsRecreated(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec(proxySpec)

	if err := os.MkdirAll(filepath.Join(h.dir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "probe", "Dockerfile"), []byte("FROM scratch AS dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
		t.Fatal(err)
	}

	ps, _ := state.Project{Dir: h.dir}.LoadState()
	subnet := netip.MustParsePrefix(ps.Segments["plain-api"])

	h.rt.containers["lab"] = []runtime.Container{{ID: "a", Service: "edge", State: "running", Health: "healthy"}}
	for _, tc := range []struct {
		ipRange netip.Prefix
		down    bool
	}{{netip.Prefix{}, true}, {compose.DynamicRange(subnet), false}} {
		h.rt.subnets = []runtime.Network{{Name: "bb-lab_plain-api", Subnet: subnet, ExperimentID: ps.ExperimentID, IPRange: tc.ipRange}}

		h.rt.calls = nil
		if _, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir}); err != nil {
			t.Fatal(err)
		}

		i, j := slices.Index(h.rt.calls, "down"), slices.Index(h.rt.calls, "up")
		if (i >= 0) != tc.down || j < i {
			t.Errorf("ip_range %v: calls = %v, want down %v before up", tc.ipRange, h.rt.calls, tc.down)
		}
	}
}
