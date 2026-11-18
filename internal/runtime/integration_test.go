//go:build integration

package runtime

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// StatsStream against the real docker CLI: a container started after the
// stream yields rows with its full id and a memory figure, so the parser
// matches what docker stats prints when streaming (control sequences
// around each row).
func TestIntegrationStatsStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows := make(chan Sample)

	go func() {
		for s, err := range New().StatsStream(ctx) {
			if err != nil {
				return
			}

			select {
			case rows <- s:
			case <-ctx.Done():
				return
			}
		}
	}()

	out, err := exec.Command("docker", "run", "-d", "--rm", "--label", "bb-runtime-test=stats", "alpine:3.24.4", "sleep", "60").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}

	id := strings.TrimSpace(string(out))

	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	for {
		select {
		case s := <-rows:
			if s.ID == id {
				if s.Usage.MemoryBytes == 0 || s.Usage.MemoryLimit == 0 {
					t.Errorf("row for the container has no memory figure: %+v", s.Usage)
				}

				return
			}
		case <-ctx.Done():
			t.Fatal("no stats row for a container started after the stream")
		}
	}
}
