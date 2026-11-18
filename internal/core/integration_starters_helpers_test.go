//go:build integration

package core

import (
	"strings"
	"testing"
)

// envoyConfigValid runs envoy --mode validate on the Envoy's own config
// file and fails on any deprecated field it warns about (D-162).
func envoyConfigValid(t *testing.T, experiment, service string) {
	t.Helper()

	out, err := execIn(t, experiment, service, "envoy --mode validate -c /etc/bb/config/envoy.yaml 2>&1")
	if err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("%s: envoy --mode validate: %v\n%s", service, err, out)
	}

	if strings.Contains(out, "Deprecated field") {
		t.Errorf("%s: envoy --mode validate warns about a deprecated field:\n%s", service, out)
	}
}
