//go:build integration

package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegrationAddApp runs the learner's first step: a lab with Postgres,
// Redis and Kafka, bb add app with defaults, then bb up. The auto-wired
// scaffold connects and pings every client over verified TLS, ensures the
// Kafka demo topic exists, logs ready and stays running so bb up succeeds.
func TestIntegrationAddApp(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "app")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	for _, typ := range []string{"postgres", "redis", "kafka"} {
		if _, err := eng.AddService(ctx, AddServiceRequest{Dir: dir, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := eng.AddApp(ctx, AddAppRequest{Dir: dir, Name: "shop"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nshop logs:\n%s", err, strings.Join(logsOf(t, eng, dir, "shop"), "\n"))
	}

	logs := strings.Join(logsOf(t, eng, dir, "shop"), "\n")
	if !strings.Contains(logs, "shop: connected and ready") {
		t.Fatalf("shop did not log connected and ready:\n%s", logs)
	}

	topics, err := eng.KafkaTopics(ctx, ExploreRequest{Dir: dir, Service: "kafka"})
	if err != nil {
		t.Fatalf("kafka topics: %v", err)
	}

	foundEvents := false

	for _, topic := range topics.Topics {
		if topic.Name == "events" && topic.Partitions == 3 {
			foundEvents = true
		}
	}

	if !foundEvents {
		t.Fatalf("expected events topic with 3 partitions, got %+v", topics.Topics)
	}
}
