//go:build integration

package core

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Labs run in parallel: each test has its own experiment name, BB_HOME and
// directories, so the only shared resource is the Docker VM. labSlots
// bounds how many labs run at once.
//
// BB_IT_PARALLEL sets the bound. Otherwise it is one lab per labMemory of
// the Docker VM's memory and at most two per CPU, between 1 and maxLabs.
// Measured on a 4-CPU, 8 GiB Docker Desktop VM: 6 labs peaked at 1.5 GiB
// and 320% CPU; 8 labs were no faster, since CPU and the daemon saturate.
// go test's -parallel flag (default GOMAXPROCS) caps the bound further.
const (
	// labMemory is what one lab may need: the largest labs (Kafka at its
	// 1 GiB limit plus apps) stay under it.
	labMemory = 1280 << 20
	maxLabs   = 8
)

var labSlots = sync.OnceValue(func() chan struct{} {
	n := 0
	if v := os.Getenv("BB_IT_PARALLEL"); v != "" {
		n, _ = strconv.Atoi(v)
	} else if out, err := exec.Command("docker", "info", "--format", "{{.MemTotal}} {{.NCPU}}").Output(); err == nil {
		var mem, cpus int64
		if _, err := fmt.Sscan(string(out), &mem, &cpus); err == nil {
			n = int(min(mem/labMemory, 2*cpus, maxLabs))
		}
	}
	return make(chan struct{}, max(n, 1))
})

// parallelLab marks the test parallel and holds a lab slot until the
// test's cleanups, the lab's down among them, have run. Call it before
// registering any cleanup that touches Docker.
func parallelLab(t *testing.T) {
	t.Helper()
	t.Parallel()
	slots := labSlots()
	start := time.Now()
	slots <- struct{}{}
	t.Cleanup(func() { <-slots })
	t.Logf("waited %s for a lab slot (%d slots)", time.Since(start).Round(time.Second), cap(slots))
}

// labName returns an experiment name for the test and, once the test and
// its down are over, removes the images compose built for it: every test
// has its own project name, so its image tags are never reused. BuildKit's
// cache keeps the layers, so a later build of the same app stays fast.
func labName(t *testing.T, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().Unix()%100000)
	t.Cleanup(func() {
		out, err := exec.Command("docker", "image", "ls", "-q", "--filter", "label=com.docker.compose.project=bb-"+name).Output()
		ids := strings.Fields(string(out))
		slices.Sort(ids)
		if ids = slices.Compact(ids); err == nil && len(ids) > 0 {
			if b, err := exec.Command("docker", append([]string{"image", "rm", "--force"}, ids...)...).CombinedOutput(); err != nil {
				t.Logf("remove the lab's images: %v\n%s", err, b)
			}
		}
	})
	return name
}
