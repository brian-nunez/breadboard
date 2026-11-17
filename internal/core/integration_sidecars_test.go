//go:build integration

package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sidecarLab creates an experiment from spec (its name line is added),
// with the probe app copied to ./probe and any extra app directories
// (directory → file → content), then runs up and requires every service
// to be healthy.
func sidecarLab(t *testing.T, prefix, spec string, extra map[string]map[string]string) (*Engine, string, string) {
	t.Helper()
	eng, dir, name := sidecarSetup(t, prefix, spec, extra)
	upHealthy(t, eng, dir)
	return eng, dir, name
}

// sidecarSetup is sidecarLab without the up.
func sidecarSetup(t *testing.T, prefix, spec string, extra map[string]map[string]string) (*Engine, string, string) {
	t.Helper()
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, prefix)
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})
		assertGone(t, name)
	})
	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}
	apps := map[string]map[string]string{"probe": {}}
	src := filepath.Join("testdata", "sidecars", "probe")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		apps["probe"][e.Name()] = readFile(t, filepath.Join(src, e.Name()))
	}
	for d, files := range extra {
		apps[d] = files
	}
	for d, files := range apps {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		for f, c := range files {
			if err := os.WriteFile(filepath.Join(dir, d, f), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(fmt.Sprintf("version: 1\nname: %s\n%s", name, spec)), 0o644); err != nil {
		t.Fatal(err)
	}
	return eng, dir, name
}

// execIn runs a shell script in a service's running container.
func execIn(t *testing.T, experiment, service, script string) (string, error) {
	t.Helper()
	return dockerMay("exec", containerOf(t, experiment, service), "sh", "-c", script)
}

// envoyAdmin reads an Envoy sidecar's admin API from inside its container.
func envoyAdmin(t *testing.T, experiment, service, path string) string {
	t.Helper()
	return docker(t, "exec", containerOf(t, experiment, service), "bash", "-c",
		fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/15000 && printf 'GET %s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n' >&3 && cat <&3`, path))
}

// envoyStat returns one counter from an Envoy sidecar's /stats.
func envoyStat(t *testing.T, experiment, service, stat string) int {
	t.Helper()
	out := envoyAdmin(t, experiment, service, "/stats?filter=^"+regexp.QuoteMeta(stat)+"$")
	m := regexp.MustCompile(regexp.QuoteMeta(stat) + `: (\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("%s has no stat %s:\n%s", service, stat, out)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// TestIntegrationEnvoyInbound: a plaintext app behind an Envoy sidecar is
// reachable from another container only over verified TLS.
func TestIntegrationEnvoyInbound(t *testing.T) {
	_, _, name := sidecarLab(t, "envoy-in", `apps:
  web:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  client:
    path: ./probe
    command: [probe, serve]
`, nil)
	out, err := execIn(t, name, "client", "probe get https://web.bb.internal:8443/")
	if err != nil || !strings.Contains(out, "instance=web proto=https") {
		t.Fatalf("TLS through the sidecar: %v\n%s", err, out)
	}
	if out, err := execIn(t, name, "client", "probe get http://web.bb.internal:8080/"); err == nil {
		t.Errorf("the app's plaintext port is reachable from another container:\n%s", out)
	}
	if out, err := execIn(t, name, "client", "probe get http://web.bb.internal:8443/"); err == nil {
		t.Errorf("the sidecar's port answered plaintext HTTP:\n%s", out)
	}
	if out, err := execIn(t, name, "web", `test "$BB_LISTEN_ADDR" = 127.0.0.1:8080 && probe get http://127.0.0.1:8080/`); err != nil {
		t.Errorf("the app is not on loopback inside its pod: %v\n%s", err, out)
	}
}

// TestIntegrationEnvoyOutboundReplicas: an app's loopback calls through
// its Envoy sidecar are balanced across every replica of the target.
func TestIntegrationEnvoyOutboundReplicas(t *testing.T) {
	_, _, name := sidecarLab(t, "envoy-out", `apps:
  web:
    path: ./probe
    command: [probe, serve]
    replicas: 3
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  caller:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
`, nil)
	var out string
	eventually(t, time.Minute, "requests to reach more than one replica", func() bool {
		var err error
		out, err = execIn(t, name, "caller", `test "$WEB_URL" = http://127.0.0.1:9001 && probe hits "$WEB_URL" 30`)
		if err != nil {
			t.Logf("hits: %v %s", err, out)
			return false
		}
		return strings.Count(out, "instance=web-") >= 2
	})
	t.Logf("distribution:\n%s", out)
}

// TestIntegrationEnvoyTransparent covers transparent mode end to end, in a
// pod that also terminates TLS (inbound), proxies (outbound) and pools
// Postgres connections:
//
//   - egress: plain HTTP to a declared target by name arrives over TLS; with
//     egress deny the app's own verified HTTPS to that target works, while
//     an undeclared TLS endpoint on the same port, an undeclared server on
//     the port of a used service, and any other host are refused; egress
//     allow passes undeclared destinations through;
//   - ingress: only the declared TLS listener answers from outside; the
//     app's plaintext port, Envoy's admin, an outbound listener and
//     PgBouncer are unreachable;
//   - no bypass: a root app process cannot become Envoy's reserved user,
//     the image's default Envoy user is not exempt, and with Envoy stopped
//     the app reaches nothing.
func TestIntegrationEnvoyTransparent(t *testing.T) {
	_, _, name := sidecarLab(t, "transp", `services:
  postgres:
    type: postgres
apps:
  web:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  web2:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  stranger:
    path: ./probe
    command: [probe, serve]
  impostor:
    path: ./probe
    command: [probe, serve, ":5432"]
  caller:
    path: ./probe
    command: [probe, serve]
    uses: [postgres]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
        outbound:
          - service: web
            port: 9001
        transparent: {egress: deny}
      - type: pgbouncer
        service: postgres
  open:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
        transparent: true
`, nil)
	rules := docker(t, "logs", strings.TrimSpace(docker(t, "ps", "-aq", "--filter", "label=breadboard.experiment="+name, "--filter", "label=breadboard.service=caller-netinit")))
	for _, want := range []string{
		"-A BB_OUT -m owner --uid-owner 1337 -j RETURN", "-A BB_OUT -p tcp -j REDIRECT --to-ports 15001", "-A OUTPUT -p tcp -j BB_OUT",
		"-A BB_IN -p tcp -m tcp --dport 8443 -j RETURN", "-A BB_IN -p tcp -j REJECT --reject-with tcp-reset", "-A INPUT -p tcp -j BB_IN",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("netinit log lacks %q:\n%s", want, rules)
		}
	}
	mustGet := func(app, url, want string) {
		t.Helper()
		if out, err := execIn(t, name, app, "probe get "+url); err != nil || !strings.Contains(out, want) {
			t.Errorf("%s: GET %s: %v\n%s", app, url, err, out)
		}
	}
	mustFail := func(app, script, why string) {
		t.Helper()
		if out, err := execIn(t, name, app, script); err == nil {
			t.Errorf("%s: %s succeeded, but %s:\n%s", app, script, why, out)
		} else {
			t.Logf("%s: %s refused: %s", app, script, strings.TrimSpace(out))
		}
	}

	// Egress.
	for _, app := range []string{"caller", "open"} {
		mustGet(app, "http://web.bb.internal:8443/", "instance=web proto=https") // TLS origination
	}
	mustGet("caller", "https://web.bb.internal:8443/", "instance=web proto=https") // the app's own TLS, verified
	mustFail("caller", "probe get https://web2.bb.internal:8443/", "web2 is not a declared target")
	mustFail("caller", "probe get http://web2.bb.internal:8443/", "web2 is not a declared target")
	mustFail("caller", "probe get http://impostor.bb.internal:5432/", "only the used postgres may be reached on 5432")
	mustFail("caller", "probe get http://stranger.bb.internal:8080/", "egress is deny")
	mustGet("open", "http://stranger.bb.internal:8080/", "instance=stranger")
	if out, err := execIn(t, name, "caller-pgbouncer", `PGPASSWORD="$BB_PG_PASSWORD" psql -h 127.0.0.1 -p 6432 -U "$BB_PG_USER" -d app -tAc "select 'pooled'"`); err != nil || strings.TrimSpace(out) != "pooled" {
		t.Errorf("PgBouncer in the deny pod could not reach the used Postgres: %v\n%s", err, out)
	}
	if n := envoyStat(t, name, "caller-envoy", "egress_denied.rbac.denied"); n < 4 {
		t.Errorf("the deny filter counted %d denials, want at least 4", n)
	}

	// Ingress: only the declared TLS listener.
	mustGet("stranger", "https://caller.bb.internal:8443/", "instance=caller proto=https")
	for _, port := range []string{"8080", "15000", "9001", "6432"} {
		mustFail("stranger", "probe dial caller.bb.internal:"+port, "only the TLS listener is declared")
	}

	// No bypass.
	if out, err := execIn(t, name, "caller", "probe asuid 1337 get http://stranger.bb.internal:8080/"); err == nil || !strings.Contains(out, "setuid") && !strings.Contains(out, "setgid") {
		t.Errorf("a root app process switched to Envoy's reserved user: %v\n%s", err, out)
	}
	if out, err := dockerMay("exec", "-u", "101", containerOf(t, name, "caller"), "probe", "get", "http://stranger.bb.internal:8080/"); err == nil {
		t.Errorf("the image's default Envoy user (101) is exempt:\n%s", out)
	}
	docker(t, "stop", containerOf(t, name, "caller-envoy"))
	for _, url := range []string{"http://web.bb.internal:8443/", "https://web.bb.internal:8443/", "http://stranger.bb.internal:8080/"} {
		mustFail("caller", "probe get "+url, "Envoy is stopped")
	}
}

// TestIntegrationPgBouncerSidecar: the app connects to PgBouncer on
// 127.0.0.1:6432 and Postgres sees PgBouncer's connections over TLS.
func TestIntegrationPgBouncerSidecar(t *testing.T) {
	_, _, name := sidecarLab(t, "pgb", `services:
  postgres:
    type: postgres
apps:
  db:
    path: ./pg
    command: [sh, -c, "trap 'exit 0' TERM; while :; do sleep 1; done"]
    uses: [postgres]
    sidecars:
      - type: pgbouncer
        service: postgres
`, map[string]map[string]string{"pg": {"Dockerfile": "FROM postgres:18.7-alpine3.24 AS dev\n"}})
	if out, err := execIn(t, name, "db", `test "$PGHOST:$PGPORT:$PGSSLMODE" = 127.0.0.1:6432:disable && case "$DATABASE_URL" in *@127.0.0.1:6432/app?sslmode=disable) ;; *) exit 1;; esac`); err != nil {
		t.Fatalf("the app's Postgres variables do not point at the pool: %v\n%s", err, out)
	}
	if out, err := execIn(t, name, "db", `psql "$DATABASE_URL" -tAc "select 'pooled'"`); err != nil || strings.TrimSpace(out) != "pooled" {
		t.Fatalf("query through PgBouncer: %v\n%s", err, out)
	}
	podIP := strings.TrimSpace(docker(t, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", containerOf(t, name, "db-pod")))
	rows := psql(t, name, "select host(a.client_addr) || '/' || s.ssl from pg_stat_activity a join pg_stat_ssl s using (pid) where a.usename = 'app'")
	if len(rows) == 0 {
		t.Fatal("Postgres sees no connection from the app's role")
	}
	for _, r := range rows {
		if r != podIP+"/true" {
			t.Errorf("connection %s, want %s/true (PgBouncer in the app's pod, over TLS)", r, podIP)
		}
	}
	t.Logf("Postgres sees: %v", rows)
}

// TestIntegrationFluentBitSidecar: a line the app writes to its shared log
// directory appears in an object in the bucket.
func TestIntegrationFluentBitSidecar(t *testing.T) {
	_, _, name := sidecarLab(t, "fbit", `services:
  s3:
    type: s3-compatible
apps:
  logger:
    path: ./probe
    command: [probe, serve]
    uses: [s3]
    sidecars:
      - type: fluent-bit
        to: s3
        bucket: logs
`, nil)
	line := fmt.Sprintf("hello-from-the-app-%d", time.Now().UnixNano())
	if out, err := execIn(t, name, "logger", `test "$BB_LOG_DIR" = /var/run/bb/logs && echo `+line+` >> "$BB_LOG_DIR/app.log"`); err != nil {
		t.Fatalf("write log line: %v\n%s", err, out)
	}
	s3 := func(path string) string {
		return docker(t, "exec", containerOf(t, name, "s3"), "sh", "-c",
			`curl -sf --cacert /etc/bb/ca.pem --aws-sigv4 aws:amz:us-east-1:s3 --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" https://s3.bb.internal:8333`+path)
	}
	keyRe := regexp.MustCompile(`<Key>(logger/[^<]+)</Key>`)
	var keys [][]string
	eventually(t, 2*time.Minute, "an object in the logs bucket", func() bool {
		keys = keyRe.FindAllStringSubmatch(s3("/logs?list-type=2"), -1)
		return len(keys) > 0
	})
	var found bool
	for _, k := range keys {
		if strings.Contains(s3("/logs/"+k[1]), line) {
			found = true
			t.Logf("line found in s3://logs/%s", k[1])
		}
	}
	if !found {
		t.Errorf("no object in the bucket contains %q (keys %v)", line, keys)
	}
}

// TestIntegrationTransparentWithRegister: a registration agent in a
// transparent pod keeps working: its gossip and RPC are exempt from the
// rules, and the registry's health check reaches the app through Envoy's
// inbound port.
func TestIntegrationTransparentWithRegister(t *testing.T) {
	_, _, name := sidecarLab(t, "transreg", `services:
  consul:
    type: consul
apps:
  web:
    path: ./probe
    command: [probe, serve]
    uses: [consul]
    register: {port: 8443}
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
        transparent: {egress: deny}
`, nil)
	rules := docker(t, "logs", strings.TrimSpace(docker(t, "ps", "-aq", "--filter", "label=breadboard.experiment="+name, "--filter", "label=breadboard.service=web-netinit")))
	for _, want := range []string{"--uid-owner 1338 -j RETURN", "--dport 8301 -j RETURN", "--dport 8443 -j RETURN"} {
		if !strings.Contains(rules, want) {
			t.Errorf("netinit log lacks %q:\n%s", want, rules)
		}
	}
	var out string
	eventually(t, time.Minute, "the registry's check of web to pass", func() bool {
		out, _ = execIn(t, name, "consul", "curl -sf --cacert /etc/bb/ca.pem https://consul.bb.internal:8501/v1/health/checks/web")
		return strings.Contains(out, `"Status":"passing"`)
	})
}

// TestIntegrationTransparentRefusesReservedUsers: a transparent app whose
// image starts as Envoy's reserved uid, by number or through a named
// account, is refused before any container starts.
func TestIntegrationTransparentRefusesReservedUsers(t *testing.T) {
	const spec = `apps:
  sneaky:
    path: ./sneaky
    command: [sleep, infinity]
    sidecars:
      - type: envoy
        transparent: {egress: deny}
`
	images := map[string]string{
		"numeric USER 1337":    "FROM alpine:3.24.4 AS dev\nUSER 1337\n",
		"named user with 1337": "FROM alpine:3.24.4 AS dev\nRUN adduser -D -H -u 1337 mesh\nUSER mesh\n",
	}
	for what, dockerfile := range images {
		t.Run(what, func(t *testing.T) {
			eng, dir, name := sidecarSetup(t, "reserved", spec, map[string]map[string]string{"sneaky": {"Dockerfile": dockerfile}})
			_, err := eng.Up(context.Background(), UpRequest{Dir: dir})
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "app sneaky") || !strings.Contains(err.Error(), "uid 1337") || !strings.Contains(err.Error(), "reserved for Envoy") {
				t.Fatalf("Up = %v, want a refusal naming the app and uid 1337", err)
			}
			t.Logf("refused: %v", err)
			if out := strings.TrimSpace(docker(t, "ps", "-aq", "--filter", "label=breadboard.experiment="+name)); out != "" {
				t.Errorf("containers were created before the refusal: %s", out)
			}
		})
	}
}

// TestIntegrationTransparentToggle: with two transparent deny pods,
// switching one off recreates its pod, so it regains ordinary outbound
// behaviour (no stale capture rules), while the other still enforces deny;
// switching it back on restores capture.
func TestIntegrationTransparentToggle(t *testing.T) {
	const on = `apps:
  web:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
  stranger:
    path: ./probe
    command: [probe, serve]
  first:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
        transparent: {egress: deny}
  second:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
        transparent: {egress: deny}
`
	eng, dir, name := sidecarLab(t, "toggle", on, nil)
	write := func(spec string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(fmt.Sprintf("version: 1\nname: %s\n%s", name, spec)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refuses := func(app string) {
		t.Helper()
		if out, err := execIn(t, name, app, "probe get http://stranger.bb.internal:8080/"); err == nil {
			t.Errorf("%s reached an undeclared destination under deny:\n%s", app, out)
		}
	}
	reachesTarget := func(app string) {
		t.Helper()
		if out, err := execIn(t, name, app, `probe get "$WEB_URL"`); err != nil || !strings.Contains(out, "instance=web proto=https") {
			t.Errorf("%s through its ambassador listener: %v\n%s", app, err, out)
		}
	}
	refuses("first")
	refuses("second")
	holder := containerOf(t, name, "first-pod")

	// Off for first only: the lab keeps its addressing (second is transparent).
	write(strings.Replace(on, "        transparent: {egress: deny}\n  second:", "  second:", 1))
	upHealthy(t, eng, dir)
	if containerOf(t, name, "first-pod") == holder {
		t.Error("first's pod holder survived the change of capture mode")
	}
	if out, err := execIn(t, name, "first", "probe get http://stranger.bb.internal:8080/"); err != nil || !strings.Contains(out, "instance=stranger") {
		t.Errorf("first, no longer transparent, cannot reach the stranger directly (stale rules?): %v\n%s", err, out)
	}
	reachesTarget("first")
	refuses("second")
	reachesTarget("second")

	// Back on: capture and deny return.
	write(on)
	upHealthy(t, eng, dir)
	refuses("first")
	reachesTarget("first")
	if out, err := execIn(t, name, "first", "probe get http://web.bb.internal:8443/"); err != nil || !strings.Contains(out, "proto=https") {
		t.Errorf("first's plain HTTP to the declared target is not captured and upgraded: %v\n%s", err, out)
	}
	refuses("second")
}

// runtimeFiles reads every file of a lab's active runtime bundle.
func runtimeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(filepath.Join(dir, ".bb", "runtime"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		out[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestIntegrationRefusedUpLeavesTheRunningLab: an update that changes a
// running sidecar's config and adds a transparent app starting as uid 1337
// is refused, and the running lab is exactly as it was: same runtime
// files, same containers.
func TestIntegrationRefusedUpLeavesTheRunningLab(t *testing.T) {
	const running = `services:
  postgres:
    type: postgres
apps:
  db:
    path: ./probe
    command: [probe, serve]
    uses: [postgres]
    sidecars:
      - type: pgbouncer
        service: postgres
`
	eng, dir, name := sidecarLab(t, "refused", running, map[string]map[string]string{
		"sneaky": {"Dockerfile": "FROM alpine:3.24.4 AS dev\nUSER 1337\n"},
	})
	ids := func() string {
		return strings.TrimSpace(docker(t, "ps", "-aq", "--no-trunc", "--filter", "label=breadboard.experiment="+name))
	}
	beforeIDs, beforeFiles := ids(), runtimeFiles(t, dir)
	update := strings.Replace(running, "        service: postgres\n", "        service: postgres\n        pool_size: 20\n", 1) + `  sneaky:
    path: ./sneaky
    command: [sleep, infinity]
    sidecars:
      - type: envoy
        transparent: {egress: deny}
`
	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(fmt.Sprintf("version: 1\nname: %s\n%s", name, update)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := eng.Up(context.Background(), UpRequest{Dir: dir})
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "app sneaky") {
		t.Fatalf("Up = %v, want the reserved-user refusal", err)
	}
	afterFiles := runtimeFiles(t, dir)
	if len(afterFiles) != len(beforeFiles) {
		t.Errorf("runtime files: %d before, %d after", len(beforeFiles), len(afterFiles))
	}
	for path, data := range beforeFiles {
		if afterFiles[path] != data {
			t.Errorf("%s changed on a refused up", path)
		}
	}
	if after := ids(); after != beforeIDs {
		t.Errorf("containers changed on a refused up:\nbefore %s\nafter  %s", beforeIDs, after)
	}
	if out, err := execIn(t, name, "db", "probe dial 127.0.0.1:6432"); err != nil {
		t.Errorf("the running PgBouncer is gone after a refused up: %v\n%s", err, out)
	}
}

// TestIntegrationSegmentToTransparentTransition: one update restores TLS
// on a plaintext Postgres edge (retiring its private segment) and enables
// the lab's first transparent pod (giving the default network a fixed
// subnet). bb up succeeds and no retired segment network is left.
func TestIntegrationSegmentToTransparentTransition(t *testing.T) {
	const before = `services:
  postgres:
    type: postgres
apps:
  pooler:
    path: ./probe
    command: [probe, serve]
    uses:
      - service: postgres
        tls: {mode: off, reason: test a segment}
  web:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
`
	eng, dir, name := sidecarLab(t, "segtr", before, nil)
	networks := func() string {
		return docker(t, "network", "ls", "--format", "{{.Name}}", "--filter", "label=breadboard.experiment="+name)
	}
	if !strings.Contains(networks(), "plain-postgres") {
		t.Fatalf("no segment network before the update:\n%s", networks())
	}
	const after = `services:
  postgres:
    type: postgres
apps:
  pooler:
    path: ./probe
    command: [probe, serve]
    uses: [postgres]
    sidecars:
      - type: envoy
        outbound:
          - service: web
            port: 9001
        transparent: {egress: deny}
  web:
    path: ./probe
    command: [probe, serve]
    sidecars:
      - type: envoy
        inbound: {port: 8443, app_port: 8080}
`
	if err := os.WriteFile(filepath.Join(dir, "experiment.yaml"), []byte(fmt.Sprintf("version: 1\nname: %s\n%s", name, after)), 0o644); err != nil {
		t.Fatal(err)
	}
	upHealthy(t, eng, dir)
	if n := networks(); strings.Contains(n, "plain-") {
		t.Errorf("a retired segment network is left:\n%s", n)
	}
	if out, err := execIn(t, name, "pooler", `probe get "$WEB_URL"`); err != nil || !strings.Contains(out, "proto=https") {
		t.Errorf("the new transparent pod does not work: %v\n%s", err, out)
	}
}
