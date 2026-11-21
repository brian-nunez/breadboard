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
