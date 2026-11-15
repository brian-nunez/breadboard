package catalog

import (
	"fmt"
	"strings"
	"time"
)

func redis() *Entry {
	const port = 6379
	return &Entry{
		ID:      "redis",
		Title:   "Redis",
		Summary: "Redis in-memory data store, TLS only on 6379, password auth.",
		Image:   "redis:8.10.3-alpine3.23",
		// Redis binds every port to the same addresses, so a plaintext port
		// cannot be confined to a private segment; v1 offers none. Plaintext
		// through a terminating proxy arrives with proxies in Phase 5.
		Listeners: []Listener{{Name: "redis", Protocol: "redis", Port: port}},
		Resources: Resources{Memory: "256m", CPUs: "0.25"},
		Secrets: []Secret{
			{Key: "password", Kind: Password, Help: "password of the default user"},
		},
		DataPath: "/data",
		Notes:    notes("redis"),
		Server: func(s Server) Container {
			start := strings.Join([]string{
				"exec docker-entrypoint.sh redis-server",
				"--port 0",
				fmt.Sprintf("--tls-port %d", port),
				"--tls-cert-file " + CertFile,
				"--tls-key-file " + KeyFile,
				"--tls-ca-cert-file " + CAFile,
				"--tls-auth-clients no",
				`--requirepass "$$REDIS_PASSWORD"`,
				"--appendonly yes",
				"--dir /data",
			}, " ")
			return Container{
				Entrypoint: []string{"/bin/sh", "-c", start},
				Environment: []EnvVar{
					{Name: "REDIS_PASSWORD", Value: s.Secret("password"), Secret: true},
				},
				Health: Health{
					Test: []string{"CMD-SHELL", fmt.Sprintf(
						`redis-cli --tls --cacert %s -h %s -p %d --no-auth-warning -a "$$REDIS_PASSWORD" ping | grep -q PONG`,
						CAFile, s.FQDN, port)},
					Interval:      10 * time.Second,
					Timeout:       5 * time.Second,
					Retries:       5,
					StartPeriod:   20 * time.Second,
					StartInterval: 1 * time.Second,
				},
			}
		},
		Client: func(c Client) []EnvVar {
			return []EnvVar{
				{Name: "REDIS_URL", Value: fmt.Sprintf("rediss://default:%s@%s:%d/0", c.Secret("password"), c.FQDN, port), Secret: true},
			}
		},
	}
}
