package catalog

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/pki"
)

func TestEnvoyConfigShapes(t *testing.T) {
	cfg := string(envoyConfig(haproxyServer()))
	for _, want := range []string{
		"admin:\n  address:\n    socket_address:\n      address: 127.0.0.1\n      port_value: 15000", // loopback only
		"port_value: 8443",
		"filename: " + CertFile,
		// Several upstreams: prefixes rewritten to /, the first is the default.
		"prefix: /api/\n                          route:\n                            cluster: api\n                            prefix_rewrite: /",
		"prefix: /\n                          route:\n                            cluster: web\n",
		"name: envoy.clusters.dns",
		"dns_refresh_rate: 5s",
		"lb_policy: ROUND_ROBIN",
		"address: web.bb.internal\n                      port_value: 8443",
		"address: api.plain.bb.internal\n                      port_value: 8080",
		"sni: web.bb.internal",
		"exact: web.bb.internal",
		"filename: " + CAFile,
		"http_health_check:",
		"no_traffic_interval: 2s",
		// Passthrough: SNI-matched chains on 9443 to a TCP proxy.
		"port_value: 9443",
		"envoy.filters.listener.tls_inspector",
		"server_names:\n              - payments.bb.internal",
		"cluster: tls_payments",
		"tcp_health_check: {}",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	// Terminate and passthrough clusters carry no upstream TLS.
	if n := strings.Count(cfg, "UpstreamTlsContext"); n != 1 {
		t.Errorf("%d upstream TLS contexts, want 1 (web only)", n)
	}

	var doc any
	if err := yaml.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatalf("not YAML: %v", err)
	}

	one := haproxyServer()
	one.Upstreams = one.Upstreams[:1]

	cfg = string(envoyConfig(one))
	if strings.Contains(cfg, "prefix_rewrite") || strings.Contains(cfg, "9443") {
		t.Errorf("a single upstream rewrites paths or listens for passthrough:\n%s", cfg)
	}
	// D-162: no deprecated cluster fields, which Envoy v1.40 warns about.
	if strings.Contains(cfg, "type: STRICT_DNS") {
		t.Errorf("the config uses the deprecated STRICT_DNS type with dns_refresh_rate:\n%s", cfg)
	}

	none := haproxyServer()

	none.Upstreams = nil
	if cfg := string(envoyConfig(none)); !strings.Contains(cfg, "status: 503") || strings.Contains(cfg, "clusters:") {
		t.Errorf("a proxy without upstreams:\n%s", cfg)
	}

	e, _ := Lookup("envoy")

	c := e.Server(haproxyServer())
	if test := strings.Join(c.Health.Test, " "); !strings.Contains(test, "/ready") || !strings.Contains(test, "-verify_return_error") {
		t.Errorf("health check = %v", c.Health.Test)
	}

	if e.DataPath != "" || e.Proxy == nil || e.Proxy.ConfigFile != "envoy.yaml" || len(e.Proxy.Modes) != 3 {
		t.Errorf("entry = %+v", e)
	}
}

func TestTraefikConfigShapes(t *testing.T) {
	static := string(traefikStaticConfig(haproxyServer()))
	for _, want := range []string{
		"address: :8443",
		"address: :9443",
		"address: 127.0.0.1:8080", // API, dashboard and ping on loopback only
		"directory: /etc/bb/traefik",
		"entryPoint: traefik",
	} {
		if !strings.Contains(static, want) {
			t.Errorf("static config lacks %q:\n%s", want, static)
		}
	}

	if strings.Contains(static, "docker") {
		t.Errorf("static config mentions docker:\n%s", static)
	}

	tlsCfg := string(traefikTLSConfig(haproxyServer()))
	if !strings.Contains(tlsCfg, "certFile: "+CertFile) || !strings.Contains(tlsCfg, "minVersion: VersionTLS12") {
		t.Errorf("tls config:\n%s", tlsCfg)
	}

	dyn := string(traefikDynamicConfig(haproxyServer()))
	for _, want := range []string{
		"rule: PathPrefix(`/api/`)",
		"rule: PathPrefix(`/`)",
		// Routers in their own namespaces: route-<route>, default-<first>.
		"route-web:",
		"route-api:",
		"default-web:",
		"prefixes:\n          - /api",
		"strategy: wrr",
		// Every replica listed by name.
		"url: https://web-1.bb.internal:8443",
		"url: https://web-3.bb.internal:8443",
		"url: http://api-2.plain.bb.internal:8080",
		"serversTransport: web",
		"serverName: web.bb.internal",
		"- " + CAFile,
		"healthCheck:",
		"rule: HostSNI(`payments.bb.internal`)",
		"passthrough: true",
		"address: payments.bb.internal:8443",
	} {
		if !strings.Contains(dyn, want) {
			t.Errorf("dynamic config lacks %q:\n%s", want, dyn)
		}
	}

	var doc any
	if err := yaml.Unmarshal([]byte(dyn), &doc); err != nil {
		t.Fatalf("not YAML: %v", err)
	}

	one := haproxyServer()

	one.Upstreams = one.Upstreams[:1]
	if dyn := string(traefikDynamicConfig(one)); strings.Contains(dyn, "stripPrefix") || strings.Contains(dyn, "tcp:") {
		t.Errorf("a single upstream strips prefixes or routes TCP:\n%s", dyn)
	}

	if static := string(traefikStaticConfig(one)); strings.Contains(static, "9443") {
		t.Errorf("a passthrough entrypoint without passthrough:\n%s", static)
	}

	e, _ := Lookup("traefik")

	c := e.Server(haproxyServer())
	if test := strings.Join(c.Health.Test, " "); !strings.Contains(test, "healthcheck") || !strings.Contains(test, "--ping") {
		t.Errorf("health check = %v", c.Health.Test)
	}
	// file replaces the dynamic config only.
	s := haproxyServer()
	s.UserConfig = []byte("# mine\n")

	r, err := e.Render(s)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range r.Files {
		if mine := string(f.Data) == "# mine\n"; mine != (f.Name == "dynamic.yml") {
			t.Errorf("%s replaced = %v", f.Name, mine)
		}
	}
}

// A terminate reason comes from experiment.yaml; whatever it contains
// stays inside comments in every generated file.
func TestProxyReasonStaysAComment(t *testing.T) {
	s := haproxyServer()

	s.Upstreams[1].Reason = "practice\ninjected: 1\r\ncr: 2\rnel: 3\u0085ls: 4\u2028ps: 5\u2029tab:\t6\x00bom:\ufeff7\x1besc: 8\x7fdel c1:\u0080\u0081\u0084\u0086\u009f end"
	for name, data := range map[string][]byte{"envoy.yaml": envoyConfig(s), "dynamic.yml": traefikDynamicConfig(s)} {
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		for _, k := range []string{"injected", "cr", "nel", "ls", "ps"} {
			if _, ok := doc[k]; ok {
				t.Errorf("%s: the reason added the key %q:\n%s", name, k, data)
			}
		}

		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "injected") && !strings.HasPrefix(strings.TrimSpace(line), "# ") {
				t.Errorf("%s: reason outside a comment: %q", name, line)
			}
		}

		if strings.ContainsAny(string(data), "\r\x00\u0085\u2028\u2029\ufeff\x1b\x7f\u0080\u0081\u0084\u0086\u009f") {
			t.Errorf("%s keeps a raw line break or control character", name)
		}
	}
}

// traefikTemplateReason holds malformed and valid Go template text.
const traefikTemplateReason = "explore {{ unfinished\nand {{ \"valid\" }} and {{ .Values }}\n{{{{ nested }} }} c1:\u0080\u0081\u009f"

// traefikCollidingServer has upstreams web and web-default: once, the
// generated routers web, web-default and web-default collided.
func traefikCollidingServer() Server {
	s := haproxyServer()
	s.Upstreams = s.Upstreams[:2]
	s.Upstreams[1].Name, s.Upstreams[1].Service = "web-default", "web-default"
	s.Upstreams[1].Host = "web-default.plain.bb.internal"
	s.Upstreams[1].Servers = []string{"web-default-1.plain.bb.internal"}
	// A hostile reason (template text and C1 controls) rides along, so the
	// pinned image proves the routers still load with it.
	s.Upstreams[1].Reason = traefikTemplateReason

	return s
}

// Generated router names never collide, whatever the routes are called.
func TestTraefikRouterNamesAreDisjoint(t *testing.T) {
	var doc struct {
		HTTP struct {
			Routers map[string]struct {
				Rule    string `yaml:"rule"`
				Service string `yaml:"service"`
			} `yaml:"routers"`
		} `yaml:"http"`
	}

	data := traefikDynamicConfig(traefikCollidingServer())
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}

	want := map[string][2]string{
		"route-web":         {"PathPrefix(`/web/`)", "web"},
		"route-web-default": {"PathPrefix(`/web-default/`)", "web-default"},
		"default-web":       {"PathPrefix(`/`)", "web"},
	}
	if len(doc.HTTP.Routers) != len(want) {
		t.Errorf("routers = %v", doc.HTTP.Routers)
	}

	for name, w := range want {
		if r := doc.HTTP.Routers[name]; r.Rule != w[0] || r.Service != w[1] {
			t.Errorf("router %s = %+v, want rule %s to %s", name, r, w[0], w[1])
		}
	}
}

// Every "{{" in the generated dynamic config is the template action that
// prints it, so the file provider's template pass yields the text as
// written; executing the file as Traefik does gives back the unescaped
// config.
func TestTraefikTemplateTextStaysLiteral(t *testing.T) {
	s := haproxyServer()
	s.Upstreams[1].Reason = traefikTemplateReason
	data := traefikDynamicConfig(s)

	tpl, err := template.New("dynamic.yml").Parse(string(data))
	if err != nil {
		t.Fatalf("the template pass fails: %v\n%s", err, data)
	}

	var out strings.Builder
	if err := tpl.Execute(&out, nil); err != nil {
		t.Fatalf("the template pass fails: %v", err)
	}

	for _, want := range []string{"explore {{ unfinished\n", `{{ "valid" }} and {{ .Values }}`, "# {{{{ nested }} }} c1:\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("after the template pass, the config lacks %q:\n%s", want, out.String())
		}
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("not YAML after the template pass: %v", err)
	}
	// Only generated content is escaped: a user's file keeps its templates.
	e, _ := Lookup("traefik")
	s.UserConfig = []byte("# {{ .Mine }}\n")

	c, err := e.Render(s)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range c.Files {
		if f.Name == traefikConfigFile && string(f.Data) != "# {{ .Mine }}\n" {
			t.Errorf("the user's file was changed: %q", f.Data)
		}
	}
	// Other YAML configs are not templated, so they are not escaped.
	if env := string(envoyConfig(s)); !strings.Contains(env, "explore {{ unfinished\n") {
		t.Errorf("envoy.yaml escaped template text:\n%s", env)
	}
}

// The YAML adapter is Comment with "# " plus the YAML line breaks: each of
// them starts a new comment line, and a byte order mark is a space.
func TestYAMLCommentDelegatesToComment(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                        "# plain",
		"a\r\nb\rc\nd":                 "# a\n# b\n# c\n# d",
		"nel\u0085ls\u2028ps\u2029end": "# nel\n# ls\n# ps\n# end",
		"bom\ufeffctl\x00\x1b\x7fend":  "# bom ctl   end",
		"trailing   \n":                "# trailing\n#",
	} {
		if got := yamlComment(in); got != want {
			t.Errorf("yamlComment(%q) = %q, want %q", in, got, want)
		}

		if plain := strings.NewReplacer("\u0085", "\n", "\u2028", "\n", "\u2029", "\n", "\ufeff", " ").Replace(in); yamlComment(in) != Comment("# ", plain) {
			t.Errorf("yamlComment(%q) is not Comment", in)
		}
	}
}

// labFiles writes a lab CA, a certificate for edge.bb.internal and the
// given files under a fresh directory laid out like /etc/bb.
func labFiles(t *testing.T, files map[string][]byte) string {
	t.Helper()

	now := time.Now()

	ca, err := pki.NewCA("bb.internal", now)
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := ca.Issue(pki.LeafRequest{DNSNames: []string{"edge.bb.internal"}, Identity: pki.Identity("bb.internal", "lab", "edge")}, now)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	all := map[string][]byte{"tls/tls.crt": leaf.CertPEM, "tls/tls.key": leaf.KeyPEM, "ca.pem": ca.CertPEM}
	for p, data := range files {
		all[p] = data
	}

	for p, data := range all {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	return dir
}

func needDocker(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs Docker")
	}

	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available")
	}
}

// TestEnvoyConfigValidates runs envoy --mode validate inside the pinned
// image on the generated config (every mode, one upstream, none), with a
// real certificate from a lab CA. It needs Docker.
func TestEnvoyConfigValidates(t *testing.T) {
	needDocker(t)
	t.Parallel()

	e, _ := Lookup("envoy")
	one, none, odd := haproxyServer(), haproxyServer(), haproxyServer()
	one.Upstreams, none.Upstreams = one.Upstreams[:1], nil
	odd.Upstreams[1].Reason = "line\nbreaks\r\u2028and: keys"

	dir := labFiles(t, map[string][]byte{
		"config/all.yaml":    envoyConfig(haproxyServer()),
		"config/one.yaml":    envoyConfig(one),
		"config/none.yaml":   envoyConfig(none),
		"config/reason.yaml": envoyConfig(odd),
	})
	for _, cfg := range []string{"all.yaml", "one.yaml", "none.yaml", "reason.yaml"} {
		out, err := exec.Command("docker", "run", "--rm", "--network", "none", "-v", dir+":/etc/bb:ro",
			e.Image, "envoy", "--mode", "validate", "-c", "/etc/bb/config/"+cfg).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "configuration '/etc/bb/config/"+cfg+"' OK") {
			t.Errorf("envoy --mode validate %s: %v\n%s", cfg, err, out)
		}

		if strings.Contains(string(out), "Deprecated field") {
			t.Errorf("envoy --mode validate %s warns about a deprecated field (D-162):\n%s", cfg, out)
		}
	}
}

// TestTraefikConfigLoads starts the pinned image on the generated static
// and dynamic configs and reads back, from its API, the routers and
// services it loaded. Traefik has no offline validation mode, but it
// refuses unknown keys and reports invalid values per router and service.
// It needs Docker.
func TestTraefikConfigLoads(t *testing.T) {
	needDocker(t)
	t.Parallel()
	t.Run("every mode", func(t *testing.T) {
		t.Parallel()

		run, api := runTraefik(t, haproxyServer(), "default-web@file")
		for _, want := range []string{"route-web@file", "default-web@file", "route-api@file"} {
			if body := api("/api/http/routers"); !strings.Contains(body, `"name":"`+want+`"`) {
				t.Errorf("router %s not loaded:\n%s", want, body)
			}
		}

		if body := api("/api/tcp/routers"); !strings.Contains(body, `"name":"tls-payments@file"`) {
			t.Errorf("TCP router not loaded:\n%s", body)
		}

		e, _ := Lookup("traefik")
		if out, err := run(e.Server(haproxyServer()).Health.Test[1:]...); err != nil {
			t.Errorf("health check: %v\n%s", err, out)
		}
		// An encoded slash in the path is refused before routing (D-204).
		if out, _ := run("wget", "-qO-", "--no-check-certificate", "https://127.0.0.1:8443/api%2F..%2Fadmin"); !strings.Contains(out, "400") {
			t.Errorf("a path with an encoded slash was not refused:\n%s", out)
		}
	})
	// A route named like the first route's fallback: every router still
	// has its own key (review M2).
	t.Run("route names", func(t *testing.T) {
		t.Parallel()

		_, api := runTraefik(t, traefikCollidingServer(), "default-web@file")
		for _, want := range []string{"route-web@file", "route-web-default@file", "default-web@file"} {
			if body := api("/api/http/routers"); !strings.Contains(body, `"name":"`+want+`"`) {
				t.Errorf("router %s not loaded:\n%s", want, body)
			}
		}
	})
	// The file provider runs the dynamic file through Go templates, comments
	// included: template text in a reason, valid or not, stays literal and
	// every router loads (review M1).
	t.Run("template text in reasons", func(t *testing.T) {
		t.Parallel()

		s := haproxyServer()
		s.Upstreams[1].Reason = traefikTemplateReason

		_, api := runTraefik(t, s, "default-web@file")
		for _, want := range []string{"route-web@file", "default-web@file", "route-api@file"} {
			if body := api("/api/http/routers"); !strings.Contains(body, `"name":"`+want+`"`) {
				t.Errorf("router %s not loaded:\n%s", want, body)
			}
		}
	})
	// Aliases across replaced and added sections load in the pinned image
	// (review F2: unexpanded, Traefik exits on an unknown anchor).
	t.Run("static file aliases", func(t *testing.T) {
		t.Parallel()

		s := haproxyServer()
		s.UserStatic = &UserFile{Path: "./edge/static.yml", Data: []byte(traefikStaticAliases)}

		run, _ := runTraefik(t, s, "default-web@file")
		if out, err := run("wget", "-qO-", fmt.Sprintf("http://127.0.0.1:%d/metrics", TraefikAPIPort)); err != nil || !strings.Contains(out, "traefik_config_reloads_total") {
			t.Errorf("/metrics: %v\n%s", err, out)
		}
	})
	// A static_file is merged into traefik.yml (D-207): Prometheus metrics
	// on the loopback API entry point, a global servers transport and a
	// JSON access log load with no warning, and /metrics answers.
	t.Run("static file", func(t *testing.T) {
		t.Parallel()

		s := haproxyServer()
		s.UserStatic = &UserFile{Path: "./edge/static.yml", Data: []byte(traefikStaticSample)}
		run, _ := runTraefik(t, s, "default-web@file")
		// No backend is reachable (--network none): the request fails, and
		// is counted per router all the same.
		_, _ = run("wget", "-qO-", "--no-check-certificate", "https://127.0.0.1:8443/elsewhere")

		out, err := run("wget", "-qO-", fmt.Sprintf("http://127.0.0.1:%d/metrics", TraefikAPIPort))
		if err != nil || !strings.Contains(out, "traefik_config_reloads_total") || !strings.Contains(out, `router="default-web@file"`) {
			t.Errorf("/metrics: %v\n%s", err, out)
		}

		if out, err := run("traefik", "healthcheck", "--configFile=/etc/bb/config/traefik.yml", "--ping"); err != nil {
			t.Errorf("health check with a static_file: %v\n%s", err, out)
		}
	})
	t.Run("no upstream", func(t *testing.T) {
		t.Parallel()

		none := haproxyServer()
		none.Upstreams = nil
		run, _ := runTraefik(t, none, "no-upstream@file")

		out, _ := run("wget", "-qO-", "--no-check-certificate", "https://127.0.0.1:8443/")
		if !strings.Contains(out, "503") {
			t.Errorf("a proxy without upstreams answered:\n%s", out)
		}
	})
}

// traefikLogProblem reports whether Traefik's log holds a configuration
// error or any warning: the aliasHeadersStrategy warning (D-179), the
// encoded-characters warning (D-204) or one a later release adds.
// Traefik's console log marks levels as ERR and WRN.
func traefikLogProblem(logs string) bool {
	for _, s := range []string{
		"Error while building configuration", "aliasHeadersStrategy is not configured",
		"can reject some encoded characters", "WRN", "ERR", "level=warn", "level=error",
	} {
		if strings.Contains(logs, s) {
			return true
		}
	}

	return false
}

// runTraefik starts Traefik on s's generated files, waits until the API
// lists router, and fails the test on any configuration error. It returns
// helpers to run a command in the container and to GET an API path.
func runTraefik(t *testing.T, s Server, router string) (func(...string) (string, error), func(string) string) {
	t.Helper()

	e, _ := Lookup("traefik")

	c, err := e.Render(s)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{}

	for _, f := range c.Files {
		rel, ok := strings.CutPrefix(f.MountPath(), "/etc/bb/")
		if !ok {
			t.Fatalf("%s mounts outside /etc/bb", f.Name)
		}

		files[rel] = f.Data
	}

	dir := labFiles(t, files)
	name := "bb-test-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))

	args := append([]string{"run", "-d", "--rm", "--name", name, "--network", "none", "-v", dir + ":/etc/bb:ro", e.Image}, c.Command...)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}

	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	run := func(cmd ...string) (string, error) {
		out, err := exec.Command("docker", append([]string{"exec", name}, cmd...)...).CombinedOutput()

		return string(out), err
	}

	api := func(path string) string {
		out, _ := run("wget", "-qO-", fmt.Sprintf("http://127.0.0.1:%d%s", TraefikAPIPort, path))

		return out
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if strings.Contains(api("/api/http/routers"), `"name":"`+router+`"`) {
			break
		}
	}

	for _, path := range []string{"/api/http/routers", "/api/http/services", "/api/http/middlewares", "/api/tcp/routers", "/api/tcp/services"} {
		if body := api(path); strings.Contains(body, `"error"`) || strings.Contains(body, `"status":"disabled"`) {
			t.Errorf("Traefik reports a configuration error in %s:\n%s", path, body)
		}
	}

	logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
	if traefikLogProblem(string(logs)) {
		t.Errorf("Traefik logged configuration problems:\n%s", logs)
	}

	return run, api
}

// traefikStaticSample is a static_file with sections Breadboard leaves to
// the user: metrics, a global servers transport and the access log.
const traefikStaticSample = `metrics:
  prometheus:
    entryPoint: traefik
    addEntryPointsLabels: true
    addRoutersLabels: true
    addServicesLabels: true
serversTransport:
  maxIdleConnsPerHost: 4
accessLog:
  format: json
`

// A static_file merges into the generated document: an empty mapping
// changes nothing, and new sections come after Breadboard's.
func TestTraefikMergeStatic(t *testing.T) {
	s := haproxyServer()
	gen := traefikStaticConfig(s)
	s.UserStatic = &UserFile{Path: "./static.yml", Data: []byte("{}\n")}

	same, err := traefikMergeStatic(s)
	if err != nil || !bytes.Equal(same, gen) {
		t.Errorf("an empty static_file changed traefik.yml (%v):\n%s", err, same)
	}

	s.UserStatic.Data = []byte(traefikStaticSample)

	merged, err := traefikMergeStatic(s)
	if err != nil {
		t.Fatal(err)
	}

	accessLog := bytes.Index(gen, []byte("accessLog:"))
	if accessLog < 0 {
		t.Fatalf("generated static config has no accessLog section:\n%s", gen)
	}

	if !bytes.HasPrefix(merged, gen[:accessLog]) {
		t.Errorf("Breadboard's sections before accessLog changed:\n%s", merged)
	}

	for _, want := range []string{"accessLog:\n  format: json\n", "# accessLog from ./static.yml, replacing Breadboard's.", "# Added from ./static.yml (services.edge.static_file), as given.\nmetrics:", "serversTransport:\n  maxIdleConnsPerHost: 4\n"} {
		if !bytes.Contains(merged, []byte(want)) {
			t.Errorf("merged traefik.yml lacks %q:\n%s", want, merged)
		}
	}

	var doc map[string]any
	if err := yaml.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("merged traefik.yml is not YAML: %v", err)
	}
}

// traefikStaticAliases moves sections around anchors: accessLog takes
// Breadboard's place after log, so the alias would come before its
// anchor unless it is expanded (review F2).
const traefikStaticAliases = `accessLog: &common {format: json}
log: *common
metrics: &m
  prometheus: {entryPoint: traefik}
`

// Aliases are expanded before sections move, so the merged document
// decodes with no alias left and both sections hold the value.
func TestTraefikMergeStaticAliases(t *testing.T) {
	s := haproxyServer()
	s.UserStatic = &UserFile{Path: "./static.yml", Data: []byte(traefikStaticAliases)}

	merged, err := traefikMergeStatic(s)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(merged, []byte("*common")) || bytes.Contains(merged, []byte("&common")) || bytes.Contains(merged, []byte("&m")) {
		t.Errorf("merged traefik.yml keeps an anchor or alias:\n%s", merged)
	}

	var doc map[string]map[string]any
	if err := yaml.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("merged traefik.yml does not decode: %v\n%s", err, merged)
	}

	for _, k := range []string{"log", "accessLog"} {
		if doc[k]["format"] != "json" {
			t.Errorf("%s = %v, want format json", k, doc[k])
		}
	}
}

// A replaced section keeps the user's comments, the trailing one too.
func TestTraefikMergeStaticComments(t *testing.T) {
	s := haproxyServer()
	s.UserStatic = &UserFile{Path: "./static.yml", Data: []byte("# head note\nlog: # line note\n  level: DEBUG\n# foot note\n")}

	merged, err := traefikMergeStatic(s)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"# head note", "# line note", "# foot note"} {
		if !bytes.Contains(merged, []byte(want)) {
			t.Errorf("merged traefik.yml lost %q:\n%s", want, merged)
		}
	}
}

// Only Traefik takes a static_file; spec learns it through ProxyInfo.
func TestStaticFileSupport(t *testing.T) {
	for _, id := range []string{"haproxy", "envoy", "traefik", "nginx", "pgbouncer"} {
		info, ok := Types(id)
		if !ok || info.Proxy == nil {
			t.Fatalf("%s is not a proxy type", id)
		}

		if info.Proxy.StaticFile != (id == "traefik") {
			t.Errorf("%s: ProxyInfo.StaticFile = %v", id, info.Proxy.StaticFile)
		}
	}

	e, _ := Lookup("haproxy")
	if _, err := e.Render(Server{Name: "edge", UserStatic: &UserFile{Path: "./s.yml", Data: []byte("{}")}}); err == nil || !strings.Contains(err.Error(), "takes no static_file") {
		t.Errorf("haproxy with a static_file: %v", err)
	}
}
