//go:build integration

package core

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// starterLab creates an experiment with the given starters applied, runs up,
// and requires every service to be healthy.
func starterLab(t *testing.T, prefix string, ids ...string) (*Engine, string, string) {
	t.Helper()
	eng := realEngine(t)
	ctx := context.Background()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().Unix()%100000)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
	})
	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	upHealthy(t, eng, dir)
	return eng, dir, name
}

func upHealthy(t *testing.T, eng *Engine, dir string) {
	t.Helper()
	start := time.Now()
	res, err := eng.Up(context.Background(), UpRequest{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Services {
		if s.Health != HealthHealthy {
			t.Fatalf("%s is %s", s.Name, s.Health)
		}
	}
	t.Logf("up took %s", time.Since(start).Round(time.Second))
}

// eventually polls cond until it holds or timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(time.Second)
	}
}

// matches returns the first capture group of re for every matching line.
func matches(lines []string, re *regexp.Regexp) []string {
	var out []string
	for _, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

func psql(t *testing.T, experiment, query string) []string {
	t.Helper()
	out := docker(t, "exec", containerOf(t, experiment, "postgres"), "psql", "-U", "postgres", "-d", "app", "-tAc", query)
	return strings.Fields(out)
}

// TestIntegrationSinkToPostgres proves at-least-once delivery made safe by an
// idempotent insert: after the sink is killed mid-stream, every produced
// order is stored exactly once.
func TestIntegrationSinkToPostgres(t *testing.T) {
	eng, dir, name := starterLab(t, "sink", "kafka/sink-to-postgres")
	stored := regexp.MustCompile(`stored id=(\S+)`)
	produced := regexp.MustCompile(`produced id=(\S+)`)

	eventually(t, 2*time.Minute, "5 stored orders", func() bool {
		return len(matches(logsOf(t, eng, dir, "sink"), stored)) >= 5
	})
	// Kill the sink between writes and commits, then bring it back.
	docker(t, "kill", containerOf(t, name, "sink"))
	upHealthy(t, eng, dir)
	before := len(matches(logsOf(t, eng, dir, "sink"), stored))
	eventually(t, time.Minute, "the restarted sink to store more", func() bool {
		return len(matches(logsOf(t, eng, dir, "sink"), stored)) > before
	})

	docker(t, "stop", containerOf(t, name, "sink-producer"))
	want := matches(logsOf(t, eng, dir, "sink-producer"), produced)
	slices.Sort(want)
	var got []string
	eventually(t, time.Minute, "the sink to catch up", func() bool {
		got = psql(t, name, "select id from orders order by id")
		return len(got) >= len(want)
	})
	if !slices.Equal(got, want) {
		t.Fatalf("stored %d orders, produced %d\nstored:   %v\nproduced: %v", len(got), len(want), got, want)
	}
	t.Logf("produced %d orders; each stored exactly once across a sink crash", len(want))
}

// TestIntegrationCacheInvalidation proves invalidate-on-write: once the
// writer stops, the reader serves every product's current price well before
// the 30-second TTL could have expired a stale entry.
func TestIntegrationCacheInvalidation(t *testing.T) {
	eng, dir, name := starterLab(t, "cache", "redis/cache-invalidation")
	updated := regexp.MustCompile(`updated id=(\S+)`)
	read := regexp.MustCompile(`(?:hit|miss) id=(\S+ price=\d+)`)

	invalidated := regexp.MustCompile(`invalidated id=(\S+)`)

	// Healthy means the processes run; go run may still be compiling. Wait
	// until the reader reads and the invalidator has caught up with a change.
	eventually(t, 3*time.Minute, "the reader to read", func() bool {
		return len(matches(logsOf(t, eng, dir, "cache-reader"), read)) > 0
	})
	eventually(t, 3*time.Minute, "the invalidator to invalidate", func() bool {
		return len(matches(logsOf(t, eng, dir, "cache-invalidator"), invalidated)) > 0
	})
	n := len(matches(logsOf(t, eng, dir, "cache-writer"), updated))
	eventually(t, time.Minute, "3 more price updates", func() bool {
		return len(matches(logsOf(t, eng, dir, "cache-writer"), updated)) >= n+3
	})
	docker(t, "stop", containerOf(t, name, "cache-writer"))
	stopped := time.Now()
	truth := map[string]string{}
	for _, row := range psql(t, name, "select id || '=' || price_cents from products") {
		id, price, _ := strings.Cut(row, "=")
		truth[id] = price
	}
	skip := len(logsOf(t, eng, dir, "cache-reader"))

	// The latest read of every product must show its current price, within a
	// window shorter than the TTL, so only invalidation can explain it.
	t.Cleanup(func() {
		if t.Failed() {
			for _, app := range []string{"cache-writer", "cache-invalidator", "cache-reader"} {
				lines := logsOf(t, eng, dir, app)
				t.Logf("%s (last 15 lines):\n%s", app, strings.Join(lines[max(0, len(lines)-15):], "\n"))
			}
			t.Logf("database: %v", truth)
		}
	})
	eventually(t, 12*time.Second, "the reader to serve current prices", func() bool {
		lines := logsOf(t, eng, dir, "cache-reader")
		latest := map[string]string{}
		for _, m := range matches(lines[min(skip, len(lines)):], read) {
			id, price, _ := strings.Cut(m, " price=")
			latest[id] = price
		}
		for id, price := range truth {
			if latest[id] != price {
				return false
			}
		}
		return true
	})
	t.Logf("reader matched the database %s after the last write", time.Since(stopped).Round(time.Second))
}
