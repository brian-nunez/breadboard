//go:build integration

package core

import (
	"context"
	"path/filepath"
	"slices"
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

func TestIntegrationStarterNatsCorePubSubRequestReply(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "natscore")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "nats/core-pubsub-request-reply"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nnats logs:\n%s", err, strings.Join(logsOf(t, eng, dir, "nats"), "\n"))
	}

	pollUntil(t, 45*time.Second, "both responder replicas handled queue-group RPCs and audit observed pricing.>", func() (bool, error) {
		reqLogs, err := natsServiceLogs(ctx, eng, dir, "nats-requester")
		if err != nil {
			return false, err
		}

		auditLogs, err := natsServiceLogs(ctx, eng, dir, "nats-audit")
		if err != nil {
			return false, err
		}

		return strings.Contains(reqLogs, "nats-responder-1=") &&
			strings.Contains(reqLogs, "nats-responder-2=") &&
			strings.Contains(auditLogs, "subject=pricing.events.quoted"), nil
	})
}

func TestIntegrationStarterNatsJetStreamWorkQueue(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "natswq")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "nats/jetstream-workqueue"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nnats logs:\n%s", err, strings.Join(logsOf(t, eng, dir, "nats"), "\n"))
	}

	pollUntil(t, 60*time.Second, "JOBS WorkQueue stream visible in NatsOverview, dedup seen, and retry+ack completed", func() (bool, error) {
		ov, err := eng.NatsOverview(ctx, ExploreRequest{Dir: dir, Service: "nats"})
		if err != nil {
			return false, err
		}

		hasJobs := slices.ContainsFunc(ov.Streams, func(st NatsStream) bool {
			return st.Name == "JOBS" && st.Retention == "workqueue" && len(st.Consumers) > 0
		})
		if !hasJobs {
			return false, nil
		}

		prodLogs, err := natsServiceLogs(ctx, eng, dir, "js-producer")
		if err != nil {
			return false, err
		}

		workLogs, err := natsServiceLogs(ctx, eng, dir, "js-worker")
		if err != nil {
			return false, err
		}

		return strings.Contains(prodLogs, "duplicate=true") &&
			strings.Contains(workLogs, "nak_retry") &&
			strings.Contains(workLogs, "acked id=") &&
			strings.Contains(workLogs, "delivered=2"), nil
	})
}

func TestIntegrationStarterNatsJetStreamKVWatch(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "natskv")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "nats/jetstream-kv-watch"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nnats logs:\n%s", err, strings.Join(logsOf(t, eng, dir, "nats"), "\n"))
	}

	pollUntil(t, 45*time.Second, "KV_flags bucket visible in NatsOverview, CAS conflict rejected, and both watchers streamed updates", func() (bool, error) {
		ov, err := eng.NatsOverview(ctx, ExploreRequest{Dir: dir, Service: "nats"})
		if err != nil {
			return false, err
		}

		hasKV := slices.ContainsFunc(ov.Streams, func(st NatsStream) bool {
			return st.Name == "KV_flags" && st.Kind == "kv" && st.Messages >= 2
		})
		if !hasKV {
			return false, nil
		}

		adminLogs, err := natsServiceLogs(ctx, eng, dir, "kv-admin")
		if err != nil {
			return false, err
		}

		watchLogs, err := natsServiceLogs(ctx, eng, dir, "kv-watcher")
		if err != nil {
			return false, err
		}

		return strings.Contains(adminLogs, "CAS stale revision=") &&
			strings.Contains(watchLogs, "kv-watcher-1 watch op=KeyValuePutOp key=flags.checkout") &&
			strings.Contains(watchLogs, "kv-watcher-2 watch op=KeyValuePutOp key=flags.checkout"), nil
	})
}

func natsServiceLogs(ctx context.Context, eng *Engine, dir, service string) (string, error) {
	lines, err := eng.Logs(ctx, LogsRequest{Dir: dir, Service: service, Tail: 200})
	if err != nil {
		return "", err
	}

	var b strings.Builder

	for line, err := range lines {
		if err != nil {
			return "", err
		}

		b.WriteString(line.Text)
		b.WriteByte('\n')
	}

	return b.String(), nil
}
