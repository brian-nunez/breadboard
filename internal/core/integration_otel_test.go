//go:build integration

package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func pollUntil(t *testing.T, timeout time.Duration, desc string, check func() (bool, error)) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	var lastErr error

	for time.Now().Before(deadline) {
		ok, err := check()
		if err == nil && ok {
			return
		}

		lastErr = err

		time.Sleep(2 * time.Second)
	}

	t.Fatalf("timed out waiting for %s (last err: %v)", desc, lastErr)
}

// TestIntegrationOtelSelectiveSignals proves:
// 1. `signals: traces` starts Tempo, Grafana (HTTPS), and OTel Collector (TLS) without Loki or Prometheus.
// 2. `host: true` routes `https://otel.<lab>.localhost:<port>/api/health` to Grafana over verified TLS.
// 3. Internal backend ports (3200) are loopback-only and refused from another container.
func TestIntegrationOtelSelectiveSignals(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "otelsel")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.AddService(ctx, AddServiceRequest{
		Dir:    dir,
		Type:   "otel",
		Config: map[string]string{"signals": "traces"},
	}); err != nil {
		t.Fatal(err)
	}

	// Enable host: true on otel and add an app to verify auto-wiring and loopback isolation.
	specPath := filepath.Join(dir, "experiment.yaml")

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(specPath, []byte(strings.Replace(string(raw), "signals: traces", "signals: traces\n    host: true", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.AddApp(ctx, AddAppRequest{Dir: dir, Name: "probe"}); err != nil {
		t.Fatal(err)
	}

	up, err := eng.Up(ctx, UpRequest{Dir: dir})
	if err != nil {
		t.Fatalf("up failed: %v\notel logs:\n%s\nprobe logs:\n%s",
			err,
			strings.Join(logsOf(t, eng, dir, "otel"), "\n"),
			strings.Join(logsOf(t, eng, dir, "probe"), "\n"),
		)
	}

	if len(up.HostURLs) == 0 || !strings.Contains(up.HostURLs[0].URL, "otel."+name+".localhost:") {
		t.Fatalf("expected host URL for otel Grafana, got %+v", up.HostURLs)
	}

	// Verify Grafana is reachable through bb-host using ONLY the Breadboard host CA.
	ca, err := eng.CA(ctx)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.Host.CertPEM) {
		t.Fatal("failed to parse host CA")
	}

	hostURL := up.HostURLs[0].URL
	hostClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DialContext: func(c context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)

				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(c, network, net.JoinHostPort("127.0.0.1", port))
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hostURL+"/api/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := hostClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s/api/health: %v", hostURL, err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Grafana /api/health status = %d, want 200", resp.StatusCode)
	}

	// Disabled backends (Loki 3100, Prometheus 9090) must NOT be listening inside otel.
	for _, port := range []int{3100, 9090} {
		if _, err := eng.rt.Exec(ctx, name, "otel", []string{"curl", "-sf", fmt.Sprintf("http://127.0.0.1:%d/", port)}); err == nil {
			t.Errorf("disabled backend port %d responded inside otel container", port)
		}
	}

	// Tempo (3200) must NOT be reachable from another container (probe).
	if _, err := eng.rt.Exec(ctx, name, "probe", []string{"sh", "-c", "nc -z -w 2 otel.bb.internal 3200"}); err == nil {
		t.Errorf("tempo port 3200 was reachable from probe container; must be loopback-only")
	}

	// Probe app connected and flushed its startup span into Tempo.
	pollUntil(t, 30*time.Second, "probe startup span in Tempo", func() (bool, error) {
		tel, err := eng.OtelTelemetry(ctx, ExploreRequest{Dir: dir, Service: "otel"})
		if err != nil {
			return false, err
		}

		return tel.TracesEnabled && !tel.LogsEnabled && !tel.MetricsEnabled && len(tel.Traces) > 0, nil
	})
}

func TestIntegrationStarterOtelTraces(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "otelt")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "otel/traces"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\notel logs:\n%s", err, strings.Join(logsOf(t, eng, dir, "otel"), "\n"))
	}

	wantServices := []string{"otel-trace-client", "otel-trace-gateway", "otel-trace-orders", "otel-trace-inventory"}

	pollUntil(t, 45*time.Second, "all 4 tracing services in Tempo", func() (bool, error) {
		tel, err := eng.OtelTelemetry(ctx, ExploreRequest{Dir: dir, Service: "otel"})
		if err != nil {
			return false, err
		}

		for _, s := range wantServices {
			if !slices.Contains(tel.TraceServices, s) {
				return false, fmt.Errorf("missing %s in %v", s, tel.TraceServices)
			}
		}

		return len(tel.Traces) > 0, nil
	})
}

func TestIntegrationStarterOtelMetrics(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "otelm")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "otel/metrics"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v", err)
	}

	pollUntil(t, 45*time.Second, "metrics from api and client in Prometheus", func() (bool, error) {
		tel, err := eng.OtelTelemetry(ctx, ExploreRequest{Dir: dir, Service: "otel"})
		if err != nil {
			return false, err
		}

		hasAPI := slices.Contains(tel.MetricServices, "otel-metrics-api")
		hasClient := slices.Contains(tel.MetricServices, "otel-metrics-client")

		hasHist := slices.Contains(tel.Metrics, "order_duration_seconds_bucket")
		if !hasAPI || !hasClient || !hasHist {
			return false, fmt.Errorf("services=%v metrics=%d", tel.MetricServices, len(tel.Metrics))
		}

		return true, nil
	})
}

func TestIntegrationStarterOtelLogs(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "otell")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "otel/logs"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v", err)
	}

	wantServices := []string{"otel-log-client", "otel-log-api", "otel-log-worker"}

	pollUntil(t, 45*time.Second, "structured logs from all 3 services in Loki", func() (bool, error) {
		tel, err := eng.OtelTelemetry(ctx, ExploreRequest{Dir: dir, Service: "otel"})
		if err != nil {
			return false, err
		}

		for _, s := range wantServices {
			if !slices.Contains(tel.LogServices, s) {
				return false, fmt.Errorf("missing %s in %v", s, tel.LogServices)
			}
		}

		return len(tel.Logs) > 0, nil
	})
}
