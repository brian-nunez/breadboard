//go:build integration

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// starterLab creates an experiment with the given starters applied, runs up,
// and requires every service to be healthy. Apps are compiled while their
// images build, so callers can expect real work within a minute of up.
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

	eventually(t, time.Minute, "5 stored orders", func() bool {
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
	eventually(t, time.Minute, "the reader to read", func() bool {
		return len(matches(logsOf(t, eng, dir, "cache-reader"), read)) > 0
	})
	eventually(t, time.Minute, "the invalidator to invalidate", func() bool {
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

// TestIntegrationS3Compatible checks the S3 Compatible entry: configured
// buckets exist, S3 is HTTPS only, SeaweedFS's internal servers are not
// reachable from other containers, and its gRPC port demands a client
// certificate.
func TestIntegrationS3Compatible(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := fmt.Sprintf("s3-%d", time.Now().Unix()%100000)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
	})
	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(fmt.Sprintf(
		"version: 1\nname: %s\nservices:\n  s3:\n    type: s3-compatible\n    config:\n      buckets: uploads,reports\n  redis:\n    type: redis\n", name)), 0o644); err != nil {
		t.Fatal(err)
	}
	upHealthy(t, eng, dir)

	s3 := containerOf(t, name, "s3")
	list := docker(t, "exec", s3, "sh", "-c", `curl -sf --cacert /etc/bb/ca.pem --aws-sigv4 aws:amz:us-east-1:s3 --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" https://s3.bb.internal:8333/`)
	for _, b := range []string{"<Name>uploads</Name>", "<Name>reports</Name>"} {
		if !strings.Contains(list, b) {
			t.Errorf("bucket %s missing from ListBuckets:\n%s", b, list)
		}
	}

	// Probe from another container on the experiment's network.
	probe := strings.Join([]string{
		"apk add -q openssl curl >/dev/null",
		`echo "http: $(curl -s -o /dev/null -w '%{http_code}' http://s3.bb.internal:8333/)"`,
		"for p in 9333 19333 8080 18080 8888 18888 8181 9101; do nc -z -w 2 s3.bb.internal $p && echo open $p; done",
		// Another lab service's certificate is signed by the same CA but is
		// not on the allowed list.
		`(printf 'PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n'; sleep 2) | openssl s_client -connect s3.bb.internal:18333 -alpn h2 -cert /other/tls.crt -key /other/tls.key -CAfile /ca.pem 2>&1 | grep -q 'alert bad certificate' && echo other-cert-rejected`,
		"echo | openssl s_client -connect s3.bb.internal:18333 2>&1 | grep -c 'certificate required'",
	}, "; ")
	other := filepath.Join(dir, ".bb", "runtime", "tls", "redis")
	out, _ := dockerMay("run", "--rm", "--network", "bb-"+name+"_default",
		"-v", other+":/other:ro", "-v", filepath.Join(dir, ".bb", "runtime", "ca.pem")+":/ca.pem:ro",
		"alpine:3.24.4", "sh", "-c", probe)
	if !strings.Contains(out, "other-cert-rejected") {
		t.Errorf("gRPC port 18333 accepted another service's certificate:\n%s", out)
	}
	if !strings.Contains(out, "http: 400") && !strings.Contains(out, "http: 000") {
		t.Errorf("plain HTTP to 8333 was not refused:\n%s", out)
	}
	if strings.Contains(out, "open ") {
		t.Errorf("internal SeaweedFS ports are reachable from another container:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "1") {
		t.Errorf("gRPC port 18333 accepted a client without a certificate:\n%s", out)
	}
}

// TestIntegrationS3ObjectsBasics proves the walkthrough end to end: the
// delimiter listing shows one level, the multipart upload completes, and a
// credential-free client downloads the exact bytes through a presigned URL.
func TestIntegrationS3ObjectsBasics(t *testing.T) {
	eng, dir, _ := starterLab(t, "objs", "s3/objects-basics")
	var lines []string
	eventually(t, time.Minute, "the walkthrough to finish", func() bool {
		lines = logsOf(t, eng, dir, "objects")
		return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "done;") })
	})
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"listed key=notes/readme.txt",
		"listed prefix=notes/2026/",
		"uploaded part=3 bytes=1048576",
		"presigned GET status=200 bytes=11534336 matches=true",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("objects log lacks %q", want)
		}
	}
	if strings.Contains(text, "listed key=notes/2026/") {
		t.Error("the delimiter listing descended into notes/2026/")
	}
}

// TestIntegrationSecondUpKeepsMounts is a regression test: a second bb up
// must not orphan the files a running, unchanged container has bind-mounted
// (on Docker Desktop they vanished, and health checks started failing).
func TestIntegrationSecondUpKeepsMounts(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := fmt.Sprintf("re-%d", time.Now().Unix()%100000)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
	})
	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AddService(ctx, AddServiceRequest{Dir: dir, Type: "redis"}); err != nil {
		t.Fatal(err)
	}
	upHealthy(t, eng, dir)
	id := containerOf(t, name, "redis")
	upHealthy(t, eng, dir)
	if again := containerOf(t, name, "redis"); again != id {
		t.Fatal("the second up recreated an unchanged container; the test needs it kept")
	}
	for _, f := range []string{"/etc/bb/ca.pem", "/etc/bb/trust-bundle.pem", "/etc/bb/tls/tls.crt", "/etc/bb/tls/tls.key"} {
		if out, err := dockerMay("exec", id, "test", "-s", f); err != nil {
			t.Errorf("%s is missing in the running container after a second up: %v %s", f, err, out)
		}
	}
	// The health check itself reads the CA file.
	if out, err := dockerMay("exec", id, "sh", "-c", `redis-cli --tls --cacert /etc/bb/ca.pem -h redis.bb.internal -p 6379 --no-auth-warning -a "$REDIS_PASSWORD" ping`); err != nil || !strings.Contains(out, "PONG") {
		t.Errorf("TLS ping after a second up failed: %v %s", err, out)
	}
}

// TestIntegrationConsulServiceDiscovery proves sidecar registration end to
// end: the caller finds the greeter by name and reaches it over verified
// TLS, and once the greeter stops, its failing check removes it from the
// healthy instances the caller sees.
func TestIntegrationConsulServiceDiscovery(t *testing.T) {
	eng, dir, name := starterLab(t, "sd", "consul/service-discovery")
	if spec := readFile(t, filepath.Join(dir, "experiment.yaml")); !strings.Contains(spec, "register:") {
		t.Fatalf("experiment.yaml has no register block:\n%s", spec)
	}
	called := regexp.MustCompile(`(called greeter at greeter\.bb\.internal:8443 \(1 healthy\): hello from greeter)`)
	eventually(t, time.Minute, "the caller to reach the greeter through Consul", func() bool {
		return len(matches(logsOf(t, eng, dir, "caller"), called)) > 0
	})
	docker(t, "stop", containerOf(t, name, "greeter"))
	skip := len(logsOf(t, eng, dir, "caller"))
	eventually(t, time.Minute, "Consul to stop returning the stopped greeter", func() bool {
		lines := logsOf(t, eng, dir, "caller")
		return slices.ContainsFunc(lines[min(skip, len(lines)):], func(l string) bool { return strings.Contains(l, "no healthy greeter instances") })
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestIntegrationConsulSelfRegistration proves the alternative: the service
// registers itself, the caller finds it by name, and stopping it removes it.
func TestIntegrationConsulSelfRegistration(t *testing.T) {
	eng, dir, name := starterLab(t, "selfreg", "consul/self-registration")
	called := regexp.MustCompile(`(called self-greeter at self-greeter\.bb\.internal:8443 \(1 healthy\): hello from self-greeter)`)
	eventually(t, time.Minute, "the caller to reach the self-registered greeter", func() bool {
		return len(matches(logsOf(t, eng, dir, "self-caller"), called)) > 0
	})
	docker(t, "stop", containerOf(t, name, "self-greeter"))
	skip := len(logsOf(t, eng, dir, "self-caller"))
	eventually(t, time.Minute, "Consul to stop returning the stopped greeter", func() bool {
		lines := logsOf(t, eng, dir, "self-caller")
		return slices.ContainsFunc(lines[min(skip, len(lines)):], func(l string) bool { return strings.Contains(l, "no healthy self-greeter instances") })
	})
	// The service received SIGTERM and removed itself.
	if !slices.ContainsFunc(logsOf(t, eng, dir, "self-greeter"), func(l string) bool { return strings.Contains(l, "deregistered") }) {
		t.Error("self-greeter did not deregister itself on SIGTERM")
	}
}
