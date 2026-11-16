package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "explore", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The fixtures are real output from the pinned images.
func TestExplorerParsers(t *testing.T) {
	tables, err := parsePostgresTables(fixture(t, "psql-tables.txt"))
	if err != nil || len(tables) != 1 || tables[0] != (PostgresTable{Schema: "public", Name: "demo", Rows: 2, Bytes: 8192}) {
		t.Errorf("postgres = %+v %v", tables, err)
	}
	if _, err := parsePostgresTables("one\ttwo\n"); err == nil {
		t.Error("malformed psql output parsed")
	}
	if tables, err := parsePostgresTables(""); err != nil || len(tables) != 0 {
		t.Errorf("empty database = %+v %v", tables, err)
	}

	keys, err := parseRedisKeys([]byte(fixture(t, "redis-scan.json")))
	if err != nil || keys.Total != 1 || len(keys.Keys) != 1 || keys.Keys[0].Key != "greeting" || keys.Keys[0].Type != "string" || keys.Keys[0].TTL <= 0 || keys.Truncated {
		t.Errorf("redis = %+v %v", keys, err)
	}
	if k, _ := parseRedisKeys([]byte(`[3,"a","hash",-1]`)); !k.Truncated || k.Keys[0].TTL != -1 {
		t.Errorf("truncated redis = %+v", k)
	}
	if _, err := parseRedisKeys([]byte("ERR wrong password")); err == nil {
		t.Error("redis error text parsed")
	}

	cat, err := parseConsulCatalog([]byte(fixture(t, "consul-catalog-services.json")), []byte(fixture(t, "consul-health-state-any.json")), []byte(fixture(t, "consul-catalog-nodes.json")))
	if err != nil {
		t.Fatal(err)
	}
	var greeter *ConsulService
	for i := range cat.Services {
		if cat.Services[i].Name == "greeter" {
			greeter = &cat.Services[i]
		}
	}
	if greeter == nil || greeter.Instances != 1 || greeter.Passing != 1 || greeter.Critical != 0 {
		t.Errorf("greeter = %+v (catalog %+v)", greeter, cat.Services)
	}
	if len(cat.Nodes) != 2 || cat.Nodes[0].Name != "consul" || cat.Nodes[1].Name != "greeter" {
		t.Errorf("nodes = %+v", cat.Nodes)
	}
	worst, err := parseConsulCatalog([]byte(`{"web":["v1"]}`),
		[]byte(`[{"ServiceName":"web","ServiceID":"a","Status":"passing"},{"ServiceName":"web","ServiceID":"a","Status":"critical"},{"ServiceName":"web","ServiceID":"b","Status":"warning"}]`), []byte(`[]`))
	if w := worst.Services[0]; err != nil || w.Name != "web" || w.Tags[0] != "v1" || w.Instances != 2 || w.Passing != 0 || w.Warning != 1 || w.Critical != 1 {
		t.Errorf("worst-check rule = %+v %v", worst, err)
	}
}

func TestExplorersRunTheServicesOwnTools(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("postgres")
	h.add("redis")
	h.add("consul")
	h.rt.execOut = map[string]string{
		"postgres":                 fixture(t, "psql-tables.txt"),
		"redis":                    fixture(t, "redis-scan.json"),
		"consul|/catalog/services": fixture(t, "consul-catalog-services.json"),
		"consul|/health/state/any": fixture(t, "consul-health-state-any.json"),
		"consul|/catalog/nodes":    fixture(t, "consul-catalog-nodes.json"),
	}
	req := func(name string) ExploreRequest { return ExploreRequest{Dir: h.dir, Service: name} }
	pg, err := h.eng.PostgresTables(h.ctx(), req("postgres"))
	if err != nil || pg.Database != "app" || len(pg.Tables) != 1 {
		t.Fatalf("PostgresTables = %+v %v", pg, err)
	}
	if _, err := h.eng.RedisKeys(h.ctx(), req("redis")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.ConsulCatalog(h.ctx(), req("consul")); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(h.rt.execs, "\n")
	for _, want := range []string{"postgres: psql -U postgres -d app", "redis: sh -c redis-cli --tls --cacert /etc/bb/ca.pem -h redis.bb.internal", "consul: curl -sf --cacert /etc/bb/ca.pem https://consul.bb.internal:8501/v1/catalog/services"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no exec like %q in:\n%s", want, joined)
		}
	}
	// Explorers are typed: asking redis for tables is not found.
	if _, err := h.eng.PostgresTables(h.ctx(), req("redis")); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong type error = %v", err)
	}
}
