package web

import (
	"strings"
	"testing"
)

// Sidecar starter pages show the sidecars and replicas bb starter writes,
// and draw the pods, replicas and app-to-app edges.
func TestSidecarStarterPages(t *testing.T) {
	s := newProject(t)

	cookie := map[string]string{"Cookie": signIn(t, s)}
	for id, wants := range map[string][]string{
		"sidecar/envoy-ambassador": {
			"replicas: 3", "inbound: {port: 8443, app_port: 8080}", "- service: ambassador-backend", "timeout: 2s", "retries: 2",
			"×3", "TLS :8443", "ambassador-caller-envoy",
		},
		"sidecar/envoy-transparent":     {"transparent: {egress: deny}", "envoy · transparent"},
		"sidecar/circuit-breaking":      {"outlier: {consecutive_5xx: 3, ejection: 30s, max_ejection_percent: 50, interval: 5s}", "retries: 0"},
		"sidecar/pgbouncer-pool":        {"type: pgbouncer", "service: postgres", "pool_mode: transaction", "pool_size: 10", "github.com/jackc/pgx/v5"},
		"sidecar/log-shipper":           {"type: fluent-bit", "to: s3-compatible", "bucket: app-logs", "github.com/aws/aws-sdk-go-v2/service/s3"},
		"sidecar/envoy-tls-termination": {"plain-web-envoy", "Read the guide"},
	} {
		body := plainText([]string{s.get(t, "/starters/"+id, cookie).Body.String()})
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("/starters/%s lacks %q", id, want)
			}
		}
	}
}
