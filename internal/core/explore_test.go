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

func TestKafkaAndS3Parsers(t *testing.T) {
	sections := splitSections(fixture(t, "kafka.txt"))
	k, err := parseKafka(sections["topics"], fixture(t, "kafka-offsets.txt"), sections["offsets"])
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Topics) != 2 || k.Topics[0].Name != "orders" || k.Topics[1].Name != "__consumer_offsets" {
		t.Fatalf("topics (user topics first) = %+v", k.Topics)
	}
	if o := k.Topics[0]; o.Partitions != 3 || o.ReplicationFactor != 1 || o.Messages != 217+648+217 || o.Internal {
		t.Errorf("orders = %+v", o)
	}
	if !k.Topics[1].Internal || k.Topics[1].Partitions != 50 {
		t.Errorf("__consumer_offsets = %+v", k.Topics[1])
	}
	if len(k.Groups) != 1 || k.Groups[0].Name != "orders-consumers" || k.Groups[0].Members != 1 || k.Groups[0].Lag != 5 || k.Groups[0].Topics[0] != "orders" {
		t.Errorf("groups = %+v", k.Groups)
	}
	idle, _ := parseKafka("", "", "\nConsumer group 'sink' has no active members.\n\nGROUP TOPIC PARTITION CURRENT-OFFSET LOG-END-OFFSET LAG CONSUMER-ID HOST CLIENT-ID\nsink events 0 10 25 15 - - -\n")
	if len(idle.Groups) != 1 || idle.Groups[0].Members != 0 || idle.Groups[0].Lag != 15 {
		t.Errorf("idle group = %+v", idle.Groups)
	}

	names, err := parseS3Buckets([]byte(fixture(t, "s3-buckets.xml")))
	if err != nil || len(names) != 2 || names[0] != "empty" || names[1] != "uploads" {
		t.Errorf("buckets = %v %v", names, err)
	}
	b, err := parseS3Objects([]byte(fixture(t, "s3-objects.xml")))
	if err != nil || b.Objects != 2 || b.Bytes != 12 || b.Truncated {
		t.Errorf("objects = %+v %v", b, err)
	}
	if _, err := parseS3Buckets([]byte("<html>")); err == nil {
		t.Error("garbage parsed as ListBuckets")
	}
}

func TestKafkaAndS3ExplorersRunTheirTools(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.add("kafka")
	h.add("s3-compatible")
	sections := splitSections(fixture(t, "kafka.txt"))
	h.rt.execOut = map[string]string{
		"kafka":                   "### topics\n" + sections["topics"] + "### offsets\n" + fixture(t, "kafka-offsets.txt") + "### groups\n" + sections["offsets"],
		"s3-compatible|list-type": fixture(t, "s3-objects.xml"),
		"s3-compatible":           fixture(t, "s3-buckets.xml"),
	}
	k, err := h.eng.KafkaTopics(h.ctx(), ExploreRequest{Dir: h.dir, Service: "kafka"})
	if err != nil || len(k.Topics) != 2 || len(k.Groups) != 1 {
		t.Fatalf("KafkaTopics = %+v %v", k, err)
	}
	s3, err := h.eng.S3Buckets(h.ctx(), ExploreRequest{Dir: h.dir, Service: "s3-compatible"})
	if err != nil || len(s3.Buckets) != 2 || s3.Buckets[1].Name != "uploads" || s3.Buckets[1].Objects != 2 {
		t.Fatalf("S3Buckets = %+v %v", s3, err)
	}
	joined := strings.Join(h.rt.execs, "\n")
	for _, want := range []string{"kafka-topics.sh --bootstrap-server kafka.bb.internal:9093 --command-config /etc/bb/config/client.properties", "kafka-get-offsets.sh", "--aws-sigv4", "https://s3-compatible.bb.internal:8333/uploads?list-type=2&max-keys=1000"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no exec containing %q", want)
		}
	}
}
