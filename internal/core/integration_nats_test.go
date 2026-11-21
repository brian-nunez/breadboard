//go:build integration

package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegrationNatsService verifies:
// 1. nats:2.15.3-alpine3.22 starts cleanly with TLS on 4222, token auth, and JetStream enabled.
// 2. bb add app wires official nats.go v1.56.0 over verified TLS (SSL_CERT_FILE) + NATS_TOKEN and connects cleanly.
// 3. Loopback monitoring port 8222 is unreachable from other containers.
// 4. NatsOverview explorer reads version 2.15.3, JetStream enabled, and active connections.
func TestIntegrationNatsService(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "natslab")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.AddService(ctx, AddServiceRequest{Dir: dir, Type: "nats"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.AddApp(ctx, AddAppRequest{Dir: dir, Name: "probe"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nnats logs:\n%s\nprobe logs:\n%s",
			err,
			strings.Join(logsOf(t, eng, dir, "nats"), "\n"),
			strings.Join(logsOf(t, eng, dir, "probe"), "\n"),
		)
	}

	// Monitoring port 8222 must be strictly loopback and unreachable from probe.
	if _, err := eng.rt.Exec(ctx, name, "probe", []string{"sh", "-c", "nc -z -w 2 nats.bb.internal 8222"}); err == nil {
		t.Error("monitoring port 8222 was reachable from probe container")
	}

	pollUntil(t, 30*time.Second, "NatsOverview reports version 2.15.3, JetStream enabled, and >= 1 active connection", func() (bool, error) {
		ov, err := eng.NatsOverview(ctx, ExploreRequest{Dir: dir, Service: "nats"})
		if err != nil {
			return false, err
		}

		return ov.Version == "2.15.3" && ov.JetStream && ov.Connections >= 1, nil
	})
}
