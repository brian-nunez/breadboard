//go:build integration

package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestIntegrationOpenSearchServiceAndHostAccess verifies:
// 1. opensearch starts cleanly with TLS on 9200, generated admin password, and config.indices pre-creation.
// 2. bb add app wires official opensearch-go/v5 over verified TLS (SSL_CERT_FILE) and connects via Info().
// 3. host: true routes https://opensearch.<lab>.localhost:<port>/_cluster/health over verified host CA TLS.
// 4. Internal transport port 9300 is strictly loopback and unreachable from other containers.
// 5. OpenSearchIndices explorer reads cluster health, version, and user indices.
func TestIntegrationOpenSearchServiceAndHostAccess(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "oslab")
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
		Type:   "opensearch",
		Config: map[string]string{"indices": "ev-demo"},
	}); err != nil {
		t.Fatal(err)
	}

	// Enable host: true on opensearch and scaffold an app using AddApp to test opensearch-go/v5 auto-wiring live.
	specPath := filepath.Join(dir, "experiment.yaml")

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}

	updated := strings.Replace(string(raw), "type: opensearch\n", "type: opensearch\n    host: true\n", 1)
	if err := os.WriteFile(specPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.AddApp(ctx, AddAppRequest{Dir: dir, Name: "probe"}); err != nil {
		t.Fatal(err)
	}

	up, err := eng.Up(ctx, UpRequest{Dir: dir})
	if err != nil {
		t.Fatalf("up failed: %v\nopensearch logs:\n%s\nprobe logs:\n%s",
			err,
			strings.Join(logsOf(t, eng, dir, "opensearch"), "\n"),
			strings.Join(logsOf(t, eng, dir, "probe"), "\n"),
		)
	}

	if len(up.HostURLs) == 0 || !strings.Contains(up.HostURLs[0].URL, "opensearch."+name+".localhost:") {
		t.Fatalf("expected host URL for opensearch, got %+v", up.HostURLs)
	}

	// Verify host: true HTTPS access using the host CA + generated admin password.
	creds, err := eng.Creds(ctx, CredsRequest{Dir: dir, Service: "opensearch"})
	if err != nil || len(creds.Services) != 1 {
		t.Fatalf("creds: %+v, err=%v", creds, err)
	}

	var adminPass string

	for _, s := range creds.Services[0].Secrets {
		if s.Key == "password" {
			adminPass = s.Value
		}
	}

	if adminPass == "" {
		t.Fatal("missing opensearch password secret")
	}

	caRes, err := eng.CA(ctx)
	if err != nil || caRes.Host == nil {
		t.Fatalf("host CA missing: %+v, err=%v", caRes, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caRes.Host.CertPEM) {
		t.Fatal("failed to parse host CA PEM")
	}

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.HostURLs[0].URL+"/_cluster/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth("admin", adminPass)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("host access to OpenSearch failed: %v", err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OpenSearch host /_cluster/health status = %d, want 200", resp.StatusCode)
	}

	// Internal transport port 9300 must NOT be reachable from another container.
	if _, err := eng.rt.Exec(ctx, name, "probe", []string{"sh", "-c", "nc -z -w 2 opensearch.bb.internal 9300"}); err == nil {
		t.Error("transport port 9300 was reachable from probe container")
	}

	// Explorer must see the pre-created ev-demo index and version 3.10.0.
	pollUntil(t, 45*time.Second, "OpenSearchIndices reports version 3.10.0 with ev-demo index", func() (bool, error) {
		ov, err := eng.OpenSearchIndices(ctx, ExploreRequest{Dir: dir, Service: "opensearch"})
		if err != nil {
			return false, err
		}

		hasDemo := slices.ContainsFunc(ov.Indices, func(idx OpenSearchIndex) bool {
			return idx.Name == "ev-demo"
		})

		return ov.Version == "3.10.0" && (ov.Status == "green" || ov.Status == "yellow") && hasDemo, nil
	})
}

func TestIntegrationStarterOpenSearchSearchBasics(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "osbasic")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "opensearch/search-basics"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nopensearch logs:\n%s\nev-indexer logs:\n%s",
			err,
			strings.Join(logsOf(t, eng, dir, "opensearch"), "\n"),
			strings.Join(logsOf(t, eng, dir, "ev-indexer"), "\n"),
		)
	}

	pollUntil(t, 60*time.Second, "ev-vehicles indexed 40 EV docs and ev-searcher logged fuzzy/filter/facets", func() (bool, error) {
		ov, err := eng.OpenSearchIndices(ctx, ExploreRequest{Dir: dir, Service: "opensearch"})
		if err != nil {
			return false, err
		}

		has40 := slices.ContainsFunc(ov.Indices, func(idx OpenSearchIndex) bool {
			return idx.Name == "ev-vehicles" && idx.Docs == 40
		})
		if !has40 {
			return false, nil
		}

		logs, err := opensearchServiceLogs(ctx, eng, dir, "ev-searcher")
		if err != nil {
			return false, err
		}

		return strings.Contains(logs, "[fuzzy BM25]") &&
			strings.Contains(logs, "[bool must+filter]") &&
			strings.Contains(logs, "[facets] total=40"), nil
	})
}

func TestIntegrationStarterOpenSearchPostgresCDCSync(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "oscdc")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "opensearch/postgres-cdc-sync"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nopensearch logs:\n%s\nprojector logs:\n%s",
			err,
			strings.Join(logsOf(t, eng, dir, "opensearch"), "\n"),
			strings.Join(logsOf(t, eng, dir, "ev-search-projector"), "\n"),
		)
	}

	pollUntil(t, 90*time.Second, "Postgres -> Kafka -> OpenSearch projected all 40 EV docs with doc_lag=0", func() (bool, error) {
		ov, err := eng.OpenSearchIndices(ctx, ExploreRequest{Dir: dir, Service: "opensearch"})
		if err != nil {
			return false, err
		}

		has40 := slices.ContainsFunc(ov.Indices, func(idx OpenSearchIndex) bool {
			return idx.Name == "ev-cdc" && idx.Docs == 40
		})
		if !has40 {
			return false, nil
		}

		logs, err := opensearchServiceLogs(ctx, eng, dir, "ev-cdc-verifier")
		if err != nil {
			return false, err
		}

		return strings.Contains(logs, "postgres(rows=40") && strings.Contains(logs, "doc_lag=0"), nil
	})
}

func TestIntegrationStarterOpenSearchZeroDowntimeReindex(t *testing.T) {
	eng := realEngine(t)
	ctx := context.Background()
	name := labName(t, "osreidx")
	dir := filepath.Join(t.TempDir(), name)
	t.Cleanup(func() {
		_, _ = eng.Down(context.Background(), DownRequest{Dir: dir, Volumes: true})

		assertGone(t, name)
	})

	if _, err := eng.Init(ctx, InitRequest{Dir: dir, Name: name}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.ApplyStarter(ctx, ApplyStarterRequest{Dir: dir, ID: "opensearch/zero-downtime-reindex"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Up(ctx, UpRequest{Dir: dir}); err != nil {
		t.Fatalf("up failed: %v\nmigrator logs:\n%s\nreader logs:\n%s",
			err,
			strings.Join(logsOf(t, eng, dir, "ev-reindex-migrator"), "\n"),
			strings.Join(logsOf(t, eng, dir, "ev-reindex-reader"), "\n"),
		)
	}

	pollUntil(t, 90*time.Second, "alias ev-registry cut over to ev-registry-v2 with 40 docs and zero reader errors", func() (bool, error) {
		ov, err := eng.OpenSearchIndices(ctx, ExploreRequest{Dir: dir, Service: "opensearch"})
		if err != nil {
			return false, err
		}

		v1Ready, v2Aliased := false, false

		for _, idx := range ov.Indices {
			if idx.Name == "ev-registry-v1" && idx.Docs == 40 && len(idx.Aliases) == 0 {
				v1Ready = true
			}

			if idx.Name == "ev-registry-v2" && idx.Docs == 40 && slices.Contains(idx.Aliases, "ev-registry") {
				v2Aliased = true
			}
		}

		if !v1Ready || !v2Aliased {
			return false, nil
		}

		logs, err := opensearchServiceLogs(ctx, eng, dir, "ev-reindex-reader")
		if err != nil {
			return false, err
		}

		return strings.Contains(logs, "backing_index=ev-registry-v2") && strings.Contains(logs, "errors=0"), nil
	})
}

func opensearchServiceLogs(ctx context.Context, eng *Engine, dir, service string) (string, error) {
	lines, err := eng.Logs(ctx, LogsRequest{Dir: dir, Service: service, Tail: 200})
	if err != nil {
		return "", err
	}

	var b strings.Builder

	for line, err := range lines {
		if err != nil {
			return "", err
		}

		b.WriteString(line.Text)
		b.WriteByte('\n')
	}

	return b.String(), nil
}
