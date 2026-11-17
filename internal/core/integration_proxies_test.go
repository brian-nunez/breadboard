//go:build integration

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationHAProxy runs HAProxy with one upstream in each TLS mode
// and checks what each mode promises:
//
//   - re-encrypt: the client verifies HAProxy's certificate, and requests
//     reach more than one of the target's 3 replicas over TLS;
//   - terminate: the app's plaintext port answers HAProxy on the private
//     segment, and no other container can reach it, even when the app
//     ignores BB_PLAIN_LISTEN_ADDR and binds every interface;
//   - passthrough: the client sees the backend's own certificate.
func TestIntegrationHAProxy(t *testing.T) {
	_, _, name := sidecarLab(t, "haproxy", `services:
  edge:
    type: haproxy
    upstreams:
      - service: web
        port: 8443
      - service: api
        tls: {mode: off, reason: practice TLS termination}
      - service: payments
        port: 8443
        tls: {mode: passthrough}
      - service: rogue
        port: 9090
        tls: {mode: off, reason: an app that binds every interface}
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
  rogue:
    path: ./probe
    command: [probe, serve, ":9090"]
    replicas: 2
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge]
`, nil)

	t.Run("re-encrypt", func(t *testing.T) {
		out, err := execIn(t, name, "client", `test "$EDGE_URL" = https://edge.bb.internal:8443 && probe peer edge.bb.internal:8443 edge.bb.internal`)
		if err != nil || !strings.Contains(out, "peer=edge.bb.internal") {
			t.Fatalf("the client did not verify HAProxy's certificate: %v\n%s", err, out)
		}
		eventually(t, time.Minute, "requests to reach more than one replica", func() bool {
			out, err = execIn(t, name, "client", `probe hits "$EDGE_URL/web/" 30`)
			if err != nil {
				t.Logf("hits: %v %s", err, out)
				return false
			}
			return strings.Count(out, "instance=web-") >= 2
		})
		t.Logf("distribution:\n%s", out)
		// The first upstream is also the default route; the backend got
		// TLS from HAProxy (it serves nothing else) and the forwarded proto.
		out, err = execIn(t, name, "client", `probe get "$EDGE_URL/anything"`)
		if err != nil || !strings.Contains(out, "instance=web-") || !strings.Contains(out, "proto=https") {
			t.Errorf("default route: %v\n%s", err, out)
		}
	})

	t.Run("terminate", func(t *testing.T) {
		var out string
		eventually(t, time.Minute, "the terminated app to answer through HAProxy", func() bool {
			var err error
			out, err = execIn(t, name, "client", `probe get "$EDGE_URL/api/"`)
			return err == nil && strings.Contains(out, "instance=api-")
		})
		addr, err := execIn(t, name, "api-1", `echo "$BB_PLAIN_LISTEN_ADDR"`)
		addr = strings.TrimSpace(addr)
		if err != nil || !strings.HasPrefix(addr, "10.213.") || !strings.HasSuffix(addr, ":8080") {
			t.Fatalf("api-1 BB_PLAIN_LISTEN_ADDR = %q (%v)", addr, err)
		}
		host, port, _ := strings.Cut(addr, ":")
		// HAProxy is on the segment: a raw TCP connection opens.
		if out, err := execIn(t, name, "edge", `bash -c 'exec 3<>/dev/tcp/`+host+`/`+port+` && echo open'`); err != nil || !strings.Contains(out, "open") {
			t.Errorf("HAProxy cannot reach the plaintext port %s: %v\n%s", addr, err, out)
		}
		// Nobody else is: not by segment address, not by the app's name.
		for _, target := range []string{addr, "api-1.bb.internal:" + port, "api.plain.bb.internal:" + port} {
			if out, err := execIn(t, name, "client", "probe dial "+target); err == nil {
				t.Errorf("another container reached the plaintext port at %s:\n%s", target, out)
			}
		}
	})

	t.Run("terminate-guard", func(t *testing.T) {
		// rogue binds 0.0.0.0:9090, ignoring BB_PLAIN_LISTEN_ADDR. Its
		// netinit's rule still confines the port to the segment address.
		if out := docker(t, "logs", containerOf(t, name, "rogue-1")); !strings.Contains(out, "listening on :9090") {
			t.Fatalf("rogue-1 does not bind every interface:\n%s", out)
		}
		var out string
		eventually(t, time.Minute, "the rogue app to answer through HAProxy", func() bool {
			var err error
			out, err = execIn(t, name, "client", `probe hits "$EDGE_URL/rogue/" 20`)
			return err == nil && strings.Contains(out, "instance=rogue-1") && strings.Contains(out, "instance=rogue-2")
		})
		for _, inst := range []string{"rogue-1", "rogue-2"} {
			seg, err := execIn(t, name, inst, `echo "${BB_PLAIN_LISTEN_ADDR%:*}"`)
			seg = strings.TrimSpace(seg)
			if err != nil || !strings.HasPrefix(seg, "10.213.") {
				t.Fatalf("%s segment address = %q (%v)", inst, seg, err)
			}
			for _, target := range []string{inst + ".bb.internal:9090", seg + ":9090"} {
				if out, err := execIn(t, name, "client", "probe dial "+target); err == nil {
					t.Errorf("another container reached %s's plaintext port at %s:\n%s", inst, target, out)
				}
			}
		}
	})

	t.Run("passthrough", func(t *testing.T) {
		var out string
		eventually(t, time.Minute, "passthrough to reach the backend", func() bool {
			var err error
			out, err = execIn(t, name, "client", `test "$EDGE_PASSTHROUGH_ADDR" = edge.bb.internal:9443 && probe peer "$EDGE_PASSTHROUGH_ADDR" payments.bb.internal`)
			return err == nil
		})
		if !strings.Contains(out, "peer=payments.bb.internal") || strings.Contains(out, "edge.bb.internal") || !strings.Contains(out, "instance=payments proto=tls") {
			t.Errorf("the client did not see the backend's own certificate:\n%s", out)
		}
		if out, err := execIn(t, name, "client", "probe peer edge.bb.internal:9443 web.bb.internal"); err == nil {
			t.Errorf("an SNI no upstream claims was routed:\n%s", out)
		}
	})

	// The generated global maxconn 256 and nbthread 1 keep HAProxy's
	// connection tables lab-sized instead of sized from the container's
	// fd limit (review M4: ~90 MiB at idle). Measured after TLS traffic in
	// every mode.
	t.Run("memory", func(t *testing.T) {
		if out, err := execIn(t, name, "client", `probe hits "$EDGE_URL/web/" 200 && probe hits "$EDGE_URL/api/" 100 && for i in 1 2 3 4 5 6 7 8 9 10; do probe peer "$EDGE_PASSTHROUGH_ADDR" payments.bb.internal || exit 1; done`); err != nil {
			t.Fatalf("traffic: %v\n%s", err, out)
		}
		edge := containerOf(t, name, "edge")
		if logs := docker(t, "logs", edge); strings.Contains(logs, "Automatically setting global.maxconn") {
			t.Errorf("HAProxy sized maxconn itself:\n%s", logs)
		}
		out, err := execIn(t, name, "edge", `for f in /proc/[0-9]*/status; do awk '/^Name:/{n=$2} /^Threads:/{th=$2} /^VmRSS:/{r=$2} END{if (n=="haproxy") print r, th}' "$f" 2>/dev/null; done; cat /sys/fs/cgroup/memory.current 2>/dev/null || echo 0`)
		if err != nil {
			t.Fatalf("reading memory: %v\n%s", err, out)
		}
		lines := strings.Fields(out)
		if len(lines) < 3 || len(lines)%2 != 1 {
			t.Fatalf("unexpected memory readout:\n%s", out)
		}
		// The master and its worker share pages, so their RSS does not add
		// up; the largest process and the container's cgroup are the measure.
		var maxKiB int
		for i := 0; i+1 < len(lines); i += 2 {
			var kib, threads int
			fmt.Sscan(lines[i], &kib)
			fmt.Sscan(lines[i+1], &threads)
			maxKiB = max(maxKiB, kib)
			t.Logf("haproxy process: VmRSS %d KiB, %d thread(s)", kib, threads)
			if threads != 1 {
				t.Errorf("an haproxy process runs %d threads, want 1 (nbthread 1)", threads)
			}
		}
		var cgroup int
		fmt.Sscan(lines[len(lines)-1], &cgroup)
		t.Logf("HAProxy after TLS traffic: largest haproxy VmRSS %d KiB; container memory.current %d KiB (limit 128 MiB)", maxKiB, cgroup/1024)
		if maxKiB > 64*1024 || cgroup > 64<<20 {
			t.Errorf("HAProxy uses more than half its 128 MiB limit: VmRSS %d KiB, memory.current %d KiB", maxKiB, cgroup/1024)
		}
	})
}

// TestIntegrationHAProxyScaleTerminateTarget scales a terminate target from
// 1 to 2 replicas while two terminating proxies hold addresses on its
// segment: the new replica's fixed address is free (proxies take the upper
// half), and both proxies reach both replicas.
func TestIntegrationHAProxyScaleTerminateTarget(t *testing.T) {
	spec := func(replicas int) string {
		return fmt.Sprintf(`services:
  edge:
    type: haproxy
    upstreams:
      - service: api
        tls: {mode: off, reason: practice TLS termination}
  edge2:
    type: haproxy
    upstreams:
      - service: api
        tls: {mode: off, reason: practice TLS termination}
apps:
  api:
    path: ./probe
    command: [probe, serve]
    replicas: %d
  client:
    path: ./probe
    command: [probe, serve]
    uses: [edge, edge2]
`, replicas)
	}
	eng, dir, name := sidecarLab(t, "haproxy-scale", spec(1), nil)
	for _, proxy := range []string{"edge", "edge2"} {
		out, err := execIn(t, name, proxy, `getent hosts `+proxy+`.bb.internal >/dev/null; cat /proc/net/fib_trie | grep -o '10\.213\.[0-9]*\.[0-9]*' | sort -u`)
		t.Logf("%s addresses: %v %s", proxy, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(fmt.Sprintf("version: 1\nname: %s\n%s", name, spec(2))), 0o644); err != nil {
		t.Fatal(err)
	}
	upHealthy(t, eng, dir)
	for _, url := range []string{"$EDGE_URL", "$EDGE2_URL"} {
		eventually(t, time.Minute, "both replicas to answer through "+url, func() bool {
			out, err := execIn(t, name, "client", `probe hits "`+url+`/" 20`)
			return err == nil && strings.Contains(out, "instance=api-1") && strings.Contains(out, "instance=api-2")
		})
	}
}
