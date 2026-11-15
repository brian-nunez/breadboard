package catalog

import (
	"encoding/json"
	"fmt"
	"time"
)

func consul() *Entry {
	const (
		httpsPort = 8501
		httpPort  = 8500
	)
	return &Entry{
		ID:      "consul",
		Title:   "Consul",
		Summary: "HashiCorp Consul service catalog and KV store: one server, HTTPS on 8501.",
		Image:   "hashicorp/consul:1.22.7",
		Listeners: []Listener{
			{Name: "https", Protocol: "https", Port: httpsPort, PlaintextPort: httpPort},
		},
		Resources: Resources{Memory: "256m", CPUs: "0.5"},
		Secrets: []Secret{
			{Key: "gossip-key", Kind: GossipKey, Help: "key that encrypts Serf gossip between agents"},
		},
		DataPath: "/consul/data",
		Notes:    notes("consul"),
		Server: func(s Server) Container {
			// Plain HTTP, gRPC, DNS, and WAN gossip are off. LAN gossip stays
			// on for Phase 2 client agents, encrypted with the gossip key, and
			// server RPC requires TLS with a certificate from the CA.
			ports := map[string]any{"http": -1, "https": httpsPort, "grpc": -1, "grpc_tls": 8503, "dns": -1, "serf_wan": -1}
			cfg := map[string]any{
				"datacenter":              "bb",
				"node_name":               s.Name,
				"server":                  true,
				"bootstrap_expect":        1,
				"ui_config":               map[string]any{"enabled": true},
				"client_addr":             "0.0.0.0",
				"bind_addr":               "{{ GetPrivateIP }}",
				"ports":                   ports,
				"encrypt_verify_incoming": true,
				"encrypt_verify_outgoing": true,
				"tls": map[string]any{
					"defaults": map[string]any{
						"ca_file":         CAFile,
						"cert_file":       CertFile,
						"key_file":        KeyFile,
						"verify_incoming": false,
						"verify_outgoing": true,
					},
					"internal_rpc": map[string]any{"verify_incoming": true, "verify_server_hostname": false},
				},
			}
			if s.Plain != nil {
				// Plain HTTP bound to the private segment's address only.
				ports["http"] = httpPort
				cfg["addresses"] = map[string]any{"http": s.Plain.Address}
			}
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				panic(err) // static types only
			}
			return Container{
				// The gossip key goes on the command line (compose resolves it in
				// dotenv mode), never into the mounted config file. Consul stores
				// it in the data directory's keyring on first start.
				Command: []string{"agent", "-config-file=" + ConfigDir + "/consul.json", "-encrypt=" + s.Secret("gossip-key")},
				Environment: []EnvVar{
					// Used by the consul CLI inside the container, including the health check.
					{Name: "CONSUL_HTTP_ADDR", Value: fmt.Sprintf("https://%s:%d", s.FQDN, httpsPort)},
					{Name: "CONSUL_CACERT", Value: CAFile},
				},
				Files: []File{{Name: "consul.json", Data: append(data, '\n')}},
				Health: Health{
					// Lists raft peers over HTTPS with CA verification; it
					// succeeds only once a leader is elected.
					Test:          []string{"CMD", "consul", "operator", "raft", "list-peers"},
					Interval:      10 * time.Second,
					Timeout:       5 * time.Second,
					Retries:       5,
					StartPeriod:   30 * time.Second,
					StartInterval: 2 * time.Second,
				},
			}
		},
		Client: func(c Client) []EnvVar {
			if c.Plain != nil {
				return []EnvVar{{Name: "CONSUL_HTTP_ADDR", Value: fmt.Sprintf("http://%s:%d", c.Plain.FQDN, httpPort)}}
			}
			return []EnvVar{
				{Name: "CONSUL_HTTP_ADDR", Value: fmt.Sprintf("https://%s:%d", c.FQDN, httpsPort)},
				{Name: "CONSUL_CACERT", Value: TrustBundleFile},
			}
		},
	}
}
