package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddServiceWithConfig(t *testing.T) {
	h := newHarness(t)
	h.init()

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "otel",
		Config: map[string]string{"signals": "traces,metrics"},
	}); err != nil {
		t.Fatalf("AddService otel with config: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(h.dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "signals: traces,metrics") {
		t.Fatalf("experiment.yaml missing config.signals:\n%s", raw)
	}

	// Invalid config value must fail validation and leave experiment.yaml untouched.
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "otel",
		Name:   "otel2",
		Config: map[string]string{"signals": "invalid"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid config error = %v, want ErrValidation", err)
	}
}

func TestOtelTelemetryExplorer(t *testing.T) {
	h := newHarness(t)
	h.init()

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "otel",
		Config: map[string]string{"signals": "logs,metrics,traces"},
	}); err != nil {
		t.Fatal(err)
	}

	h.rt.execOut = map[string]string{
		"otel|/api/search?limit=20":                   `{"traces":[{"traceID":"abc123","rootServiceName":"otel-obs-client","rootTraceName":"checkout","durationMs":42,"spanSet":{"matched":4}}]}`,
		"otel|/api/search/tag/service.name/values":    `{"tagValues":["otel-obs-api","otel-obs-client"]}`,
		"otel|/api/v1/label/__name__/values":          `{"status":"success","data":["api_orders_total","orders_db_insert_seconds_bucket"]}`,
		"otel|/api/v1/label/service_name/values":      `{"status":"success","data":["otel-obs-api","otel-obs-orders"]}`,
		"otel|/loki/api/v1/label/service_name/values": `{"status":"success","data":["otel-obs-api","otel-obs-worker"]}`,
		"otel|/loki/api/v1/query_range":               `{"data":{"result":[{"stream":{"service_name":"otel-obs-worker","severity_text":"INFO","trace_id":"abc123"},"values":[["1700000000000000000","order fulfilled"]]}]}}`,
	}

	got, err := h.eng.OtelTelemetry(h.ctx(), ExploreRequest{Dir: h.dir, Service: "otel"})
	if err != nil {
		t.Fatalf("OtelTelemetry: %v", err)
	}

	if !got.LogsEnabled || !got.MetricsEnabled || !got.TracesEnabled {
		t.Fatalf("expected all signals enabled: %+v", got)
	}

	if len(got.Traces) != 1 || got.Traces[0].TraceID != "abc123" || got.Traces[0].Spans != 4 {
		t.Errorf("traces = %+v", got.Traces)
	}

	if len(got.Metrics) != 2 || got.Metrics[0] != "api_orders_total" {
		t.Errorf("metrics = %+v", got.Metrics)
	}

	if len(got.Logs) != 1 || got.Logs[0].TraceID != "abc123" || got.Logs[0].Body != "order fulfilled" {
		t.Errorf("logs = %+v", got.Logs)
	}
}
