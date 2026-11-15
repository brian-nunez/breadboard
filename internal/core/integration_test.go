//go:build integration

// Integration tests run against real Docker:
//
//	go test -tags integration -timeout 30m ./internal/core/
package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func realEngine(t *testing.T) *Engine {
	t.Helper()
	os.Unsetenv("BB_DOMAIN")
	eng, err := New(Options{Home: filepath.Join(t.TempDir(), "home"), HealthTimeout: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.rt.Available(context.Background()); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	return eng
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func dockerMay(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return string(out), err
}

func containerOf(t *testing.T, experiment, service string) string {
	t.Helper()
	id := strings.TrimSpace(docker(t, "ps", "-q", "--filter", "label=breadboard.experiment="+experiment, "--filter", "label=breadboard.service="+service))
	if id == "" {
		t.Fatalf("no container for %s", service)
	}
	return id
}

// assertGone checks that nothing labeled for the experiment survives.
func assertGone(t *testing.T, experiment string) {
	t.Helper()
	label := "label=breadboard.experiment=" + experiment
	for _, args := range [][]string{
		{"ps", "-aq", "--filter", label},
		{"network", "ls", "-q", "--filter", label},
		{"volume", "ls", "-q", "--filter", label},
	} {
		if out := strings.TrimSpace(docker(t, args...)); out != "" {
			t.Errorf("docker %s still lists: %s", strings.Join(args, " "), out)
		}
	}
}

func logsOf(t *testing.T, eng *Engine, dir, service string) []string {
	t.Helper()
	lines, err := eng.Logs(context.Background(), LogsRequest{Dir: dir, Service: service})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for l, err := range lines {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, l.Text)
	}
	return out
}

var recordRe = regexp.MustCompile(`(produced|consumed) .*key=(\S+) partition=(\d+) offset=(\d+)`)

func records(lines []string, verb string) []string {
	var out []string
	for _, l := range lines {
		if m := recordRe.FindStringSubmatch(l); m != nil && m[1] == verb {
			out = append(out, fmt.Sprintf("%s/%s@%s", m[2], m[3], m[4]))
		}
	}
	slices.Sort(out)
	return out
}

func TestIntegrationPhaseOne(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := fmt.Sprintf("it-%d", time.Now().Unix()%100000)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"kafka", "postgres", "redis", "consul"} {
		if _, err := eng.AddService(ctx, AddServiceRequest{Dir: dir, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "kafka/producer-consumer"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := eng.Up(ctx, UpRequest{Dir: dir, Events: func(e Event) { t.Logf("event: %s", e.Message) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("up took %s", time.Since(start).Round(time.Second))
	// Every health check connects over TLS with full verification, so
	// healthy also proves the certificates work.
	for _, s := range res.Services {
		if s.Health != HealthHealthy {
			t.Fatalf("%s is %s", s.Name, s.Health)
		}
	}
	if len(res.Services) != 6 {
		t.Fatalf("services = %d", len(res.Services))
	}

	// Let the producer write, stop it, and check the consumer received
	// exactly the records the broker acknowledged: none missing, none extra.
	deadline := time.Now().Add(2 * time.Minute)
	for len(records(logsOf(t, eng, dir, "producer"), "produced")) < 10 {
		if time.Now().After(deadline) {
			t.Fatal("producer did not write 10 records in time")
		}
		time.Sleep(time.Second)
	}
	docker(t, "stop", containerOf(t, name, "producer"))
	produced := records(logsOf(t, eng, dir, "producer"), "produced")
	var consumed []string
	deadline = time.Now().Add(time.Minute)
	for {
		consumed = records(logsOf(t, eng, dir, "consumer"), "consumed")
		if len(consumed) >= len(produced) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if !slices.Equal(produced, consumed) {
		t.Fatalf("produced %d records, consumed %d\nproduced: %v\nconsumed: %v", len(produced), len(consumed), produced, consumed)
	}
	t.Logf("producer sent %d records; consumer received exactly %d", len(produced), len(consumed))

	// TLS is the only way in: plaintext connections are refused.
	pg := containerOf(t, name, "postgres")
	if out, err := dockerMay("exec", pg, "sh", "-c", `PGPASSWORD="$BB_APP_PASSWORD" psql "host=postgres.bb.internal dbname=app user=app sslmode=disable" -c "select 1"`); err == nil {
		t.Errorf("postgres accepted a plaintext connection:\n%s", out)
	}
	if out, err := dockerMay("exec", containerOf(t, name, "redis"), "redis-cli", "-h", "redis.bb.internal", "-p", "6379", "ping"); err == nil && strings.Contains(out, "PONG") {
		t.Errorf("redis answered plaintext: %s", out)
	}

	if _, err := eng.Down(ctx, DownRequest{Dir: dir, Volumes: true}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, name)

	// The exported stack runs with plain docker compose, without bb.
	out := filepath.Join(t.TempDir(), "export")
	if _, err := eng.ExportCompose(ctx, ExportComposeRequest{Dir: dir, Out: out, Secrets: "dotenv"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = dockerMay("compose", "--project-directory", out, "-f", filepath.Join(out, "docker-compose.yml"), "-f", filepath.Join(out, "docker-compose.dev.yml"), "down", "--volumes")
	})
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.yml", "-f", "docker-compose.dev.yml", "up", "-d", "--wait", "--wait-timeout", "600")
	cmd.Dir = out
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("exported stack did not reach healthy: %v\n%s", err, b)
	}
	down := exec.Command("docker", "compose", "-f", "docker-compose.yml", "-f", "docker-compose.dev.yml", "down", "--volumes")
	down.Dir = out
	if b, err := down.CombinedOutput(); err != nil {
		t.Fatalf("export down: %v\n%s", err, b)
	}
	assertGone(t, name)
}

// TestIntegrationPlaintextSegment proves a plaintext edge is reachable only
// from the client that asked for it.
func TestIntegrationPlaintextSegment(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := fmt.Sprintf("pt-%d", time.Now().Unix()%100000)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
	})
	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "probe")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Dockerfile": "FROM golang:1.27.4-alpine3.24 AS dev\nRUN apk add --no-cache postgresql18-client\nWORKDIR /src\nCOPY . .\nCMD [\"go\", \"run\", \".\"]\n",
		"go.mod":     "module probe\n\ngo 1.27.4\n",
		"main.go":    "package main\n\nimport \"net/http\"\n\nfunc main() {\n\thttp.HandleFunc(\"/healthz\", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(\"ok\")) })\n\t_ = http.ListenAndServe(\":8080\", nil)\n}\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(app, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec := fmt.Sprintf(`version: 1
name: %s
services:
  postgres:
    type: postgres
  kafka:
    type: kafka
  consul:
    type: consul
apps:
  pooler:
    path: ./probe
    uses:
      - service: postgres
        tls: {mode: off, reason: terminate TLS at a pooler}
      - service: kafka
        tls: {mode: off, reason: compare throughput}
      - service: consul
        tls: {mode: off, reason: debug with curl}
  outsider:
    path: ./probe
    uses: [postgres, kafka, consul]
`, name)
	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := eng.Up(ctx, UpRequest{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 3 {
		t.Errorf("warnings = %v", res.Warnings)
	}
	psql := func(service, conn string) (string, error) {
		return dockerMay("exec", containerOf(t, name, service), "sh", "-c", `psql "`+conn+`" -tAc "select 1"`)
	}
	if out, err := psql("pooler", "host=postgres.plain.bb.internal sslmode=disable"); err != nil {
		t.Errorf("pooler could not use its plaintext edge: %v\n%s", err, out)
	}
	if out, err := psql("outsider", "host=postgres.plain.bb.internal sslmode=disable"); err == nil {
		t.Errorf("outsider reached the private segment:\n%s", out)
	}
	if out, err := psql("outsider", "host=postgres.bb.internal sslmode=disable"); err == nil {
		t.Errorf("outsider connected without TLS on the shared network:\n%s", out)
	}
	if out, err := psql("outsider", "host=postgres.bb.internal sslmode=verify-full sslrootcert=/etc/bb/ca.pem"); err != nil {
		t.Errorf("outsider could not connect over TLS: %v\n%s", err, out)
	}
	sh := func(service, script string) (string, error) {
		return dockerMay("exec", containerOf(t, name, service), "sh", "-c", script)
	}
	// Kafka's plaintext listener is bound to the segment address only.
	if out, err := sh("pooler", "nc -z -w 5 kafka.plain.bb.internal 9092"); err != nil {
		t.Errorf("pooler could not reach Kafka's plaintext listener: %v\n%s", err, out)
	}
	if out, err := sh("outsider", "nc -z -w 5 kafka.plain.bb.internal 9092"); err == nil {
		t.Errorf("outsider reached Kafka's plaintext listener on the segment:\n%s", out)
	}
	if out, err := sh("outsider", "nc -z -w 5 kafka.bb.internal 9092"); err == nil {
		t.Errorf("Kafka's plaintext port answers on the shared network:\n%s", out)
	}
	// Consul's plain HTTP is bound to the segment address only.
	if out, err := sh("pooler", "wget -qO- http://consul.plain.bb.internal:8500/v1/status/leader"); err != nil {
		t.Errorf("pooler could not use Consul over plain HTTP: %v\n%s", err, out)
	}
	if out, err := sh("outsider", "wget -qO- -T 5 http://consul.bb.internal:8500/v1/status/leader"); err == nil {
		t.Errorf("Consul plain HTTP answers on the shared network:\n%s", out)
	}
	if _, err := eng.Down(ctx, DownRequest{Dir: dir, Volumes: true}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, name)
}

// TestIntegrationDownRemovesVolumesOfRemovedServices covers a service removed
// between two ups: its volume is no longer in the compose files, so compose
// alone would leave it behind on down --volumes.
func TestIntegrationDownRemovesVolumesOfRemovedServices(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := fmt.Sprintf("rm-%d", time.Now().Unix()%100000)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
	})
	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"postgres", "redis"} {
		if _, err := eng.AddService(ctx, AddServiceRequest{Dir: dir, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RemoveService(ctx, RemoveServiceRequest{Dir: dir, Name: "postgres"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if all := docker(t, "volume", "ls", "-q", "--filter", "label=breadboard.experiment="+name); !strings.Contains(all, "postgres-data") {
		t.Fatalf("precondition: the removed service's volume should survive up; volumes:\n%s", all)
	}
	if _, err := eng.Down(ctx, DownRequest{Dir: dir, Volumes: true}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, name)
}
