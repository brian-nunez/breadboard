package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAddServiceOpenSearchWithConfig(t *testing.T) {
	h := newHarness(t)
	h.init()

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "opensearch",
		Config: map[string]string{"indices": "ev-vehicles,ev-events"},
	}); err != nil {
		t.Fatalf("AddService opensearch with config: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(h.dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	data := string(raw)
	if !strings.Contains(data, "type: opensearch") || !strings.Contains(data, "indices: ev-vehicles,ev-events") {
		t.Fatalf("experiment.yaml missing opensearch config:\n%s", data)
	}

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:    h.dir,
		Type:   "opensearch",
		Name:   "search2",
		Config: map[string]string{"indices": "_invalid"},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid indices error = %v, want ErrValidation", err)
	}
}

func TestOpenSearchIndicesExplorer(t *testing.T) {
	h := newHarness(t)
	h.init()

	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{
		Dir:  h.dir,
		Type: "opensearch",
	}); err != nil {
		t.Fatal(err)
	}

	h.rt.execOut = map[string]string{
		"opensearch|/_cluster/health":                       `{"cluster_name":"opensearch","status":"green","active_shards":3,"unassigned_shards":0}`,
		"opensearch|/_cat/indices":                          `[{"index":".opendistro_security","health":"green","status":"open","pri":"1","rep":"0","docs.count":"10","docs.deleted":"0","store.size":"4096"},{"index":"ev-vehicles-v2","health":"green","status":"open","pri":"1","rep":"0","docs.count":"40","docs.deleted":"2","store.size":"28672"},{"index":"ev-vehicles-v1","health":"yellow","status":"open","pri":"1","rep":"1","docs.count":"40","docs.deleted":"0","store.size":"24576"}]`,
		"opensearch|/_cat/aliases":                          `[{"alias":"ev-read","index":"ev-vehicles-v2"},{"alias":"ev-write","index":"ev-vehicles-v2"}]`,
		"opensearch|https://opensearch.bb.internal:9200/\"": `{"version":{"number":"3.10.0"}}`,
	}

	got, err := h.eng.OpenSearchIndices(h.ctx(), ExploreRequest{Dir: h.dir, Service: "opensearch"})
	if err != nil {
		t.Fatalf("OpenSearchIndices: %v", err)
	}

	if got.ClusterName != "opensearch" || got.Status != "green" || got.Version != "3.10.0" {
		t.Fatalf("unexpected overview header: %+v", got)
	}

	if got.ActiveShards != 3 || got.UnassignedShards != 0 {
		t.Fatalf("unexpected shard counts: %+v", got)
	}

	if len(got.Indices) != 2 {
		t.Fatalf("expected 2 user indices (dot index filtered), got %+v", got.Indices)
	}

	if got.Indices[0].Name != "ev-vehicles-v1" || got.Indices[1].Name != "ev-vehicles-v2" {
		t.Fatalf("indices not sorted: %+v", got.Indices)
	}

	if got.Indices[1].Docs != 40 || got.Indices[1].Deleted != 2 || got.Indices[1].Bytes != 28672 {
		t.Fatalf("unexpected v2 stats: %+v", got.Indices[1])
	}

	if !slices.Equal(got.Indices[1].Aliases, []string{"ev-read", "ev-write"}) {
		t.Fatalf("v2 aliases = %v", got.Indices[1].Aliases)
	}
}

func TestApplyOpenSearchStarters(t *testing.T) {
	h := newHarness(t)
	h.init()

	for _, id := range []string{"opensearch/search-basics", "opensearch/postgres-cdc-sync", "opensearch/zero-downtime-reindex"} {
		res, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: id})
		if err != nil {
			t.Fatalf("apply %s: %v", id, err)
		}

		if len(res.Written) == 0 {
			t.Errorf("%s wrote no files", id)
		}
	}

	raw, err := os.ReadFile(filepath.Join(h.dir, "experiment.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	specText := string(raw)
	for _, want := range []string{
		"type: opensearch",
		"host: true",
		"ev-indexer:",
		"ev-searcher:",
		"ev-catalog-writer:",
		"ev-search-projector:",
		"ev-cdc-verifier:",
		"ev-reindex-migrator:",
		"ev-reindex-reader:",
	} {
		if !strings.Contains(specText, want) {
			t.Errorf("experiment.yaml missing %q:\n%s", want, specText)
		}
	}
}
