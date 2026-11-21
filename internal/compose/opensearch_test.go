package compose

import (
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
)

func TestComposeOpenSearchServiceAndHostAccess(t *testing.T) {
	s := &spec.Spec{
		Version: 1,
		Name:    "search-lab",
		Services: []spec.Service{
			{
				Name:   "opensearch",
				Type:   "opensearch",
				Config: map[string]string{"indices": "ev-vehicles"},
				Host:   &spec.Host{},
			},
		},
		Apps: []spec.App{
			{Name: "api", Path: "./api", Command: []string{"api"}, Uses: uses("opensearch")},
		},
	}

	b := render(t, s, secrets.Dotenv)

	var composeYML, osConf, hostDyn string

	for _, f := range b.Files {
		switch f.Path {
		case "docker-compose.yml":
			composeYML = string(f.Data)
		case "config/opensearch/opensearch.yml":
			osConf = string(f.Data)
		case "config/bb-host/dynamic.yml":
			hostDyn = string(f.Data)
		}
	}

	if !strings.Contains(composeYML, "opensearchproject/opensearch:3.10.0") {
		t.Errorf("compose missing pinned opensearch image")
	}

	if !strings.Contains(composeYML, "OPENSEARCH_URL: https://opensearch.bb.internal:9200") {
		t.Errorf("app missing OPENSEARCH_URL")
	}

	if !strings.Contains(composeYML, "BB_INDICES: ev-vehicles") {
		t.Errorf("compose missing BB_INDICES config env")
	}

	if !strings.Contains(osConf, "transport.host: 127.0.0.1") || !strings.Contains(osConf, "http.publish_host: opensearch.bb.internal") {
		t.Errorf("opensearch.yml missing loopback transport or publish_host:\n%s", osConf)
	}

	if !strings.Contains(hostDyn, "opensearch.search-lab.localhost") || !strings.Contains(hostDyn, "https://opensearch.bb.internal:9200") {
		t.Errorf("bb-host should route opensearch.search-lab.localhost to :9200:\n%s", hostDyn)
	}
}
