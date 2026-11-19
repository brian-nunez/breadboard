//go:build integration

package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIntegrationStarterLBConsulTraefik: Traefik routes to the replicas it
// discovers in Consul. Two replicas added with bb up join without Traefik
// being recreated; a stopped replica leaves the routes within a bounded
// time, and returns once it and its agent run again.
func TestIntegrationStarterLBConsulTraefik(t *testing.T) {
	eng, dir, name := sidecarStarterLab(t, "lb-ct", "lb/consul-traefik")
	lastLines(t, eng, dir, "ct-client", "ct-edge")

	// reached lists the replicas that answered in the client's windows from
	// line skip on.
	reached := func(skip int) map[string]bool {
		seen := map[string]bool{}

		for _, w := range lbWindows(logsOf(t, eng, dir, "ct-client"), "", skip) {
			for r := range w.replicas {
				seen[r] = true
			}
		}

		return seen
	}
	all := func(seen map[string]bool, n int) bool {
		for i := 1; i <= n; i++ {
			if !seen["ct-backend-"+strconv.Itoa(i)] {
				return false
			}
		}

		return len(seen) == n
	}

	eventually(t, 3*time.Minute, "answers from all three replicas through Traefik", func() bool { return all(reached(0), 3) })

	// Scaling: the new replicas' agents register them, and Traefik finds
	// them in Consul with no change of its own.
	edge := containerOf(t, name, "ct-edge")
	specPath := filepath.Join(dir, "experiment.yaml")

	src := readFile(t, specPath)
	if !strings.Contains(src, "replicas: 3") {
		t.Fatalf("experiment.yaml has no replicas: 3:\n%s", src)
	}

	if err := os.WriteFile(specPath, []byte(strings.Replace(src, "replicas: 3", "replicas: 5", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	upHealthy(t, eng, dir)

	if got := containerOf(t, name, "ct-edge"); got != edge {
		t.Errorf("bb up recreated ct-edge after scaling the backend")
	}
	// Replicas 4 and 5 never answered before the scale-up, so every window
	// is read (the client may have been recreated, which restarts its log).
	eventually(t, 2*time.Minute, "answers from all five replicas", func() bool { return all(reached(0), 5) })

	// A stopped replica's agent goes with its network namespace; Consul
	// marks it critical and Traefik drops it on its next read.
	backend, agent := containerOf(t, name, "ct-backend-2"), containerOf(t, name, "ct-backend-2-agent")
	skip := len(logsOf(t, eng, dir, "ct-client"))
	stopped := time.Now()

	docker(t, "stop", backend)
	eventually(t, time.Minute, "three windows in a row without ct-backend-2 or a failure", func() bool {
		run := 0

		for _, w := range lbWindows(logsOf(t, eng, dir, "ct-client"), "", skip) {
			if w.allOK() && w.replicas["ct-backend-2"] == 0 {
				run++
			} else {
				run = 0
			}
		}

		return run >= 3
	})
	t.Logf("ct-backend-2 left the routes within %s", time.Since(stopped).Round(time.Second))

	// Back: the replica starts again, and its agent restarts to join the
	// replica's new network namespace and register it.
	skip = len(logsOf(t, eng, dir, "ct-client"))

	docker(t, "start", backend)
	docker(t, "restart", agent)
	eventually(t, 90*time.Second, "ct-backend-2 answering again", func() bool { return reached(skip)["ct-backend-2"] })
}
