//go:build integration

package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegrationAddApp runs the learner's first step: a lab with Postgres
// and Redis, bb add app with defaults, bb up. main is empty (Brian's
// direction, D-217), so the program exits at once and bb up reports the
// app as failed (D-058), naming it. Its init check still passed: the
// logs carry no missing-variable message, so bb up injected every
// variable the scaffold expects.
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

	for _, typ := range []string{"postgres", "redis"} {
		if _, err := eng.AddService(ctx, AddServiceRequest{Dir: dir, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := eng.AddApp(ctx, AddAppRequest{Dir: dir, Name: "shop"}); err != nil {
		t.Fatal(err)
	}

	_, err := eng.Up(ctx, UpRequest{Dir: dir})
	if !errors.Is(err, ErrUnhealthy) || !strings.Contains(err.Error(), "shop ") {
		t.Fatalf("up = %v, want ErrUnhealthy naming shop, which exits", err)
	}

	t.Logf("up reported: %v", err)

	logs := strings.Join(logsOf(t, eng, dir, "shop"), "\n")
	if strings.Contains(logs, "cannot start") || strings.Contains(logs, "missing environment variables") {
		t.Fatalf("shop's init check failed:\n%s", logs)
	}
}
