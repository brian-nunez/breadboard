package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddServiceNatsWithConfig(t *testing.T) {
	h := newHarness(t)
	h.init()

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "nats",
		Config: map[string]string{"jetstream": "false"},
	}); err != nil {
		t.Fatalf("AddService nats with config: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(h.dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	data := string(raw)
	if !strings.Contains(data, "type: nats") || !strings.Contains(data, "jetstream: \"false\"") && !strings.Contains(data, "jetstream: false") {
		t.Fatalf("experiment.yaml missing nats config:\n%s", data)
	}

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "nats",
		Name:   "nats2",
		Config: map[string]string{"jetstream": "invalid"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid jetstream error = %v, want ErrValidation", err)
	}
}

func TestNatsOverviewExplorer(t *testing.T) {
	h := newHarness(t)
	h.init()

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:  h.dir,
		Type: "nats",
	}); err != nil {
		t.Fatal(err)
	}

	h.rt.execOut = map[string]string{
		"nats|/varz": `{"version":"2.15.3","connections":3,"subscriptions":14,"in_msgs":120,"out_msgs":240,"in_bytes":8192,"out_bytes":16384}`,
		"nats|/jsz":  `{"memory":1024,"storage":4096,"account_details":[{"stream_detail":[{"name":"KV_flags","config":{"subjects":["$KV.flags.>"],"retention":"limits"},"state":{"messages":4,"bytes":512}},{"name":"JOBS","config":{"subjects":["jobs.*"],"retention":"workqueue"},"state":{"messages":2,"bytes":300},"consumer_detail":[{"name":"workers","num_pending":2,"num_ack_pending":1,"delivered":{"stream_seq":18}}]}]}]}`,
	}

	got, err := h.eng.NatsOverview(h.ctx(), ExploreRequest{Dir: h.dir, Service: "nats"})
	if err != nil {
		t.Fatalf("NatsOverview: %v", err)
	}

	if got.Version != "2.15.3" || got.Connections != 3 || got.Subscriptions != 14 || !got.JetStream {
		t.Fatalf("unexpected overview header: %+v", got)
	}

	if got.StorageBytes != 4096 || len(got.Streams) != 2 {
		t.Fatalf("unexpected streams: %+v", got)
	}

	if got.Streams[0].Name != "JOBS" || got.Streams[0].Kind != "stream" || got.Streams[0].Retention != "workqueue" {
		t.Fatalf("unexpected JOBS stream: %+v", got.Streams[0])
	}

	if len(got.Streams[0].Consumers) != 1 || got.Streams[0].Consumers[0].Delivered != 18 {
		t.Fatalf("unexpected JOBS consumers: %+v", got.Streams[0].Consumers)
	}

	if got.Streams[1].Name != "KV_flags" || got.Streams[1].Kind != "kv" {
		t.Fatalf("unexpected KV stream: %+v", got.Streams[1])
	}
}
