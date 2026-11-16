package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/state"
)

// An app with replicas runs, reports, logs, and gets certificates as named
// instances.
func TestUpWithReplicas(t *testing.T) {
	h := setupStarter(t)
	spec := strings.Replace(h.read("experiment.yaml"), "    command: [consumer]\n", "    command: [consumer]\n    replicas: 3\n", 1)
	if !strings.Contains(spec, "replicas: 3") {
		t.Fatalf("could not add replicas:\n%s", spec)
	}
	h.writeSpec(spec)
	res, err := h.eng.Up(h.ctx(), UpRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range res.Services {
		names = append(names, s.Name)
		if s.Health != HealthHealthy {
			t.Errorf("%s = %s", s.Name, s.Health)
		}
	}
	if want := []string{"kafka", "producer", "consumer-1", "consumer-2", "consumer-3"}; !slices.Equal(names, want) {
		t.Fatalf("services = %v, want %v", names, want)
	}
	if c := res.Services[3]; c.Kind != "app" || len(c.Uses) != 1 || c.Uses[0].To != "kafka" {
		t.Errorf("consumer-2 = %+v", c)
	}
	compose := string(h.rt.filesSeen["docker-compose.yml"])
	if !strings.Contains(compose, "aliases: [consumer-2.bb.internal, consumer.bb.internal]") {
		t.Errorf("compose lacks the shared alias:\n%s", compose)
	}

	// Each replica's certificate names the instance and the shared name.
	certPEM, _, ok, err := (state.Project{Dir: h.dir}).ReadCert("consumer-2")
	if !ok || err != nil {
		t.Fatalf("consumer-2 certificate: %v %v", ok, err)
	}
	cert, err := pki.ParseCert(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cert.DNSNames, []string{"consumer-2.bb.internal", "consumer.bb.internal"}) {
		t.Errorf("SANs = %v", cert.DNSNames)
	}

	// bb logs consumer means every instance; one instance can be asked for.
	for req, want := range map[string][]string{"consumer": {"consumer-1", "consumer-2", "consumer-3"}, "consumer-2": {"consumer-2"}} {
		if _, err := h.eng.Logs(h.ctx(), LogsRequest{Dir: h.dir, Service: req}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(h.rt.logArgs, want) {
			t.Errorf("logs %s asked for %v, want %v", req, h.rt.logArgs, want)
		}
	}

	d, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "consumer"})
	if err != nil || d.Name != "consumer-1" {
		t.Errorf("details of an app with replicas = %+v %v", d, err)
	}
	k, err := h.eng.ServiceDetails(h.ctx(), ServiceDetailsRequest{Dir: h.dir, Name: "kafka"})
	if err != nil || !slices.Equal(k.UsedBy, []string{"producer", "consumer"}) {
		t.Errorf("kafka used by = %v %v", k.UsedBy, err)
	}
}

// The pod holder is not a service of its own, but while it is down the app
// reports its state.
func TestPodHolderStateCountsTowardItsApp(t *testing.T) {
	app := &model.Service{Name: "web", Kind: model.App, Pod: "web-pod"}
	running := runtime.Container{Service: "web", State: "running", Health: "healthy", StartedAt: time.Unix(1, 0)}
	for name, tc := range map[string]struct {
		holder     *runtime.Container
		wantHealth string
	}{
		"holder healthy":   {&runtime.Container{Service: "web-pod", State: "running", Health: "healthy"}, HealthHealthy},
		"holder starting":  {&runtime.Container{Service: "web-pod", State: "running", Health: "starting"}, HealthStarting},
		"holder exited":    {&runtime.Container{Service: "web-pod", State: "exited"}, "exited"},
		"holder not there": {nil, StateNotCreated},
	} {
		byService := map[string]runtime.Container{"web": running}
		if tc.holder != nil {
			byService["web-pod"] = *tc.holder
		}
		c, ok := podState(app, byService)
		if got := health(c, ok); got != tc.wantHealth {
			t.Errorf("%s: health = %s, want %s", name, got, tc.wantHealth)
		}
	}
	if got := containerNames(app); !slices.Equal(got, []string{"web", "web-pod"}) {
		t.Errorf("containers of a pod app = %v", got)
	}
	plain := &model.Service{Name: "api", Kind: model.App}
	if c, ok := podState(plain, map[string]runtime.Container{"api": {Service: "api", State: "running"}}); !ok || c.Service != "api" {
		t.Error("an app outside a pod reports its own container")
	}
}
