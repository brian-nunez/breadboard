//go:build integration

package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationStarterProxy applies a starter that brings an nginx proxy
// with its own config, runs up, and reaches both replicas through the
// proxy: the shipped file is the config NGINX runs, and its backend names,
// rendered with the lab domain, resolve.
func TestIntegrationStarterProxy(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "st-proxy")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	st, render := proxyFixture(t)
	if _, err := eng.applyStarter(ctx, st, render, ApplyStarterRequest{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	upHealthy(t, eng, dir)

	out, err := execIn(t, name, "fixture-client", `test "$FIXTURE_EDGE_URL" = https://fixture-edge.bb.internal:8443 && probe get "$FIXTURE_EDGE_URL/fixture"`)
	if err != nil || !strings.Contains(out, "shipped nginx.conf") {
		t.Fatalf("the proxy does not run the shipped config: %v\n%s", err, out)
	}

	eventually(t, time.Minute, "requests through the proxy to reach both replicas", func() bool {
		out, err = execIn(t, name, "fixture-client", `probe hits "$FIXTURE_EDGE_URL/" 20`)
		if err != nil {
			t.Logf("hits: %v %s", err, out)

			return false
		}

		return strings.Contains(out, "instance=fixture-web-1") && strings.Contains(out, "instance=fixture-web-2")
	})
	t.Logf("distribution through the shipped config:\n%s", out)
}
