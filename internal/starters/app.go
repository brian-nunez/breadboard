package starters

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	templates "github.com/brian-nunez/breadboard/starters"
)

// AppTemplate is the embedded template directory of bb add app's program:
// one Go module with main.go at its root, built into one binary named after
// the app.
const AppTemplate = "go/app"

// AppScaffold is what bb add app renders: the app and, for each service in
// its uses, the variables bb up gives it.
type AppScaffold struct {
	Name       string // app name: the binary and the command; the module is example.com/<Name>
	Experiment string
	Uses       []AppUse
	// Taken are the names experiment.yaml already uses (services and
	// apps), so the README's add-a-service example names a new one.
	Taken []string
}

// AppUse is one service in the app's uses, with its env contract exactly as
// the app receives it.
type AppUse struct {
	Service   string
	Type      string // catalog type
	Title     string // catalog title
	Summary   string // catalog summary
	Plaintext bool   // the edge has TLS off
	Vars      []AppVar
}

// AppVar is one variable the app receives from a used service.
type AppVar struct {
	Name   string // as injected, prefixed when another used service has the same name
	Base   string // the catalog contract's name, before any prefix
	Value  string // empty for secrets
	Secret bool
}

// sdkImports maps every catalog type to the package of the official client
// library the starters use, at their version (in the scaffold's go.mod).
// HTTP proxies map to "": the standard library's net/http is their client.
// TestEveryCatalogTypeHasAnSDK fails when a type is missing.
var sdkImports = map[string]string{
	"postgres":      "github.com/jackc/pgx/v5/pgxpool",
	"pgbouncer":     "github.com/jackc/pgx/v5/pgxpool",
	"redis":         "github.com/redis/go-redis/v9",
	"kafka":         "github.com/twmb/franz-go/pkg/kgo",
	"s3-compatible": "github.com/aws/aws-sdk-go-v2/service/s3",
	"consul":        "github.com/hashicorp/consul/api",
	"otel":          "go.opentelemetry.io/otel",
	"haproxy":       "",
	"envoy":         "",
	"traefik":       "",
	"nginx":         "",
}

// SDKImport returns the client package of a catalog type, and whether the
// type is known; the package is empty for HTTP proxies.
func SDKImport(typ string) (string, bool) {
	imp, ok := sdkImports[typ]

	return imp, ok
}

// envHelp says what each variable of a catalog env contract holds.
// TestEveryContractVariableIsDescribed fails when one is missing.
var envHelp = map[string]string{
	"DATABASE_URL":                     "connection URL: user, password, host, port, database, sslmode",
	"PGHOST":                           "host name",
	"PGPORT":                           "port",
	"PGUSER":                           "user",
	"PGPASSWORD":                       "password",
	"PGDATABASE":                       "database",
	"PGSSLMODE":                        "TLS mode",
	"PGSSLROOTCERT":                    "CA file that signed the server's certificate",
	"REDIS_URL":                        "connection URL with the password; rediss:// means TLS",
	"KAFKA_BROKERS":                    "broker addresses, host:port, comma-separated",
	"KAFKA_SECURITY_PROTOCOL":          "SSL (TLS) or PLAINTEXT",
	"S3_ENDPOINT":                      "S3 API endpoint",
	"S3_REGION":                        "region",
	"S3_ACCESS_KEY_ID":                 "access key id",
	"S3_SECRET_ACCESS_KEY":             "secret access key",
	"S3_FORCE_PATH_STYLE":              "address buckets by path, not by subdomain",
	"AWS_ENDPOINT_URL_S3":              "S3 endpoint, read by the AWS SDKs",
	"AWS_REGION":                       "region, read by the AWS SDKs",
	"AWS_ACCESS_KEY_ID":                "access key id, read by the AWS SDKs",
	"AWS_SECRET_ACCESS_KEY":            "secret access key, read by the AWS SDKs",
	"CONSUL_HTTP_ADDR":                 "HTTP API address, read by the Consul API client",
	"CONSUL_CACERT":                    "CA bundle the Consul API client verifies with",
	"OTEL_EXPORTER_OTLP_ENDPOINT":      "OTLP gRPC endpoint over TLS (4317), read by official OTel SDKs",
	"OTEL_EXPORTER_OTLP_HTTP_ENDPOINT": "OTLP HTTP endpoint over TLS (4318)",
	"OTEL_EXPORTER_OTLP_PROTOCOL":      "default OTLP wire protocol (grpc)",
	"OTEL_EXPORTER_OTLP_CERTIFICATE":   "CA file verifying the OTLP collector certificate",
	"OTEL_TRACES_EXPORTER":             "otlp when traces are enabled on the otel service, else none",
	"OTEL_METRICS_EXPORTER":            "otlp when metrics are enabled on the otel service, else none",
	"OTEL_LOGS_EXPORTER":               "otlp when logs are enabled on the otel service, else none",
	"GRAFANA_URL":                      "Grafana HTTPS address inside the lab",
	"GRAFANA_USER":                     "Grafana admin username",
	"GRAFANA_PASSWORD":                 "password",
}

// EnvHelp says what a contract variable holds. Proxy variables are named
// after their service, so they match by suffix.
func EnvHelp(base string) (string, bool) {
	if h, ok := envHelp[base]; ok {
		return h, true
	}

	switch {
	case strings.HasSuffix(base, "_PASSTHROUGH_ADDR"):
		return "host:port for TLS passthrough routes (by SNI)", true
	case strings.HasSuffix(base, "_URL"):
		return "HTTPS address of the proxy", true
	}

	return "", false
}

// EnvBase is the contract name of a variable an app receives from service:
// name itself, or name without the service prefix bb up adds when another
// used service has the same variable.
func EnvBase(service, name string) string {
	prefix := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(service)) + "_"
	if base, ok := strings.CutPrefix(name, prefix); ok {
		if _, known := envHelp[base]; known {
			return base
		}
	}

	return name
}

type appImport struct {
	Path string
	For  string // the services that use it, comma-separated
}

type appVarView struct {
	AppVar
	Pad  string
	Help string
}

type appUseView struct {
	AppUse
	Vars []appVarView
}

// appWire describes one service's auto-wired client handle in main.go.
type appWire struct {
	Service string
	Kind    string // postgres, redis, kafka, s3, consul, http
	Var     string // primary Go variable name (e.g. db, cache, producer)
	Var2    string // secondary Go variable name (kafka: consumer)
	Env     map[string]string
}

// appData is what the scaffold's templates can use.
type appData struct {
	Name, Experiment                  string
	GoVersion, GoImage, AlpineVersion string
	Uses                              []appUseView
	Wires                             []appWire
	Imports                           []appImport // official client packages, for README
	NeedPostgres                      bool
	NeedRedis                         bool
	NeedKafka                         bool
	NeedS3                            bool
	NeedConsul                        bool
	NeedOtel                          bool
	NeedHTTP                          bool
	Example                           string // the README's add-a-service type, free as a name; empty for a generic example
}

// exampleTypes are the types the README may suggest adding, in order:
// each works on its own, so the example is valid in any lab.
var exampleTypes = []string{"redis", "postgres", "kafka", "consul", "s3-compatible", "otel"}

// exampleType is the first of exampleTypes whose name no service or app in
// the lab (or in the app's uses) takes, so `bb add <type>` and the uses it
// shows are valid; empty when every one is taken.
func exampleType(a AppScaffold) string {
	for _, t := range exampleTypes {
		if !slices.Contains(a.Taken, t) && !slices.ContainsFunc(a.Uses, func(u AppUse) bool { return u.Service == t }) && t != a.Name {
			return t
		}
	}

	return ""
}

// RenderApp renders bb add app's program for a: main.go with the lab map,
// requireEnv, auto-wired SDK clients for every used service, and wiring
// helpers below main.
func RenderApp(a AppScaffold) ([]File, error) {
	return renderApp(templates.FS, a)
}

func renderApp(fsys fs.FS, a AppScaffold) ([]File, error) {
	d := appData{Name: a.Name, Experiment: a.Experiment, GoVersion: GoVersion, GoImage: GoImage, AlpineVersion: AlpineVersion, Example: exampleType(a)}
	importFor := map[string][]string{}

	var importOrder []string

	width := 0

	for _, u := range a.Uses {
		for _, v := range u.Vars {
			width = max(width, len(v.Name))
		}
	}

	takenVars := reservedAppVars()

	for _, u := range a.Uses {
		imp, ok := SDKImport(u.Type)
		if !ok {
			return nil, fmt.Errorf("app %s: no client library for %s (type %s)", a.Name, u.Service, u.Type)
		}

		view := appUseView{AppUse: u}
		for _, v := range u.Vars {
			help, _ := EnvHelp(v.Base)
			if help == "" {
				help = "see bb learn " + u.Type
			}

			view.Vars = append(view.Vars, appVarView{AppVar: v, Pad: v.Name + strings.Repeat(" ", width-len(v.Name)), Help: help})
		}

		d.Uses = append(d.Uses, view)

		w, err := buildWire(u, takenVars, &d)
		if err != nil {
			return nil, fmt.Errorf("app %s: %w", a.Name, err)
		}

		d.Wires = append(d.Wires, w)

		if imp == "" {
			continue // an HTTP proxy: net/http is the client
		}

		if _, seen := importFor[imp]; !seen {
			importOrder = append(importOrder, imp)
		}

		if !slices.Contains(importFor[imp], u.Service) {
			importFor[imp] = append(importFor[imp], u.Service)
		}
	}

	for _, imp := range importOrder {
		d.Imports = append(d.Imports, appImport{Path: imp, For: strings.Join(importFor[imp], ", ")})
	}

	var files []File

	err := fs.WalkDir(fsys, AppTemplate, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}

		if !strings.HasSuffix(p, ".tmpl") {
			return fmt.Errorf("app template: %s is missing the .tmpl suffix", p)
		}

		rel := strings.TrimSuffix(strings.TrimPrefix(p, AppTemplate+"/"), ".tmpl")

		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}

		t, err := template.New(path.Base(p)).Option("missingkey=error").Parse(string(src))
		if err != nil {
			return fmt.Errorf("app template: %w", err)
		}

		var buf bytes.Buffer
		if err := t.Execute(&buf, d); err != nil {
			return fmt.Errorf("app template: %w", err)
		}

		data := buf.Bytes()
		if strings.HasSuffix(rel, ".go") {
			// gofmt the result, so the learner's first diff is their own.
			if data, err = format.Source(data); err != nil {
				return fmt.Errorf("app template: %s: %w", rel, err)
			}
		}

		files = append(files, File{Path: rel, Data: data})

		return nil
	})
	if err != nil {
		return nil, err
	}

	return files, nil
}

func buildWire(u AppUse, taken map[string]bool, d *appData) (appWire, error) {
	byBase := map[string]string{}

	var urlVar string

	for _, v := range u.Vars {
		byBase[v.Base] = v.Name
		if urlVar == "" && strings.HasSuffix(v.Base, "_URL") {
			urlVar = v.Name
		}
	}

	camel := camelIdentifier(u.Service)

	switch u.Type {
	case "postgres", "pgbouncer":
		d.NeedPostgres = true

		base := camel + "DB"
		if u.Service == "postgres" {
			base = "db"
		}

		return appWire{
			Service: u.Service,
			Kind:    "postgres",
			Var:     claimVar(taken, base),
			Env:     map[string]string{"url": byBase["DATABASE_URL"]},
		}, nil
	case "redis":
		d.NeedRedis = true

		base := camel + "Cache"
		if u.Service == "redis" {
			base = "cache"
		}

		return appWire{
			Service: u.Service,
			Kind:    "redis",
			Var:     claimVar(taken, base),
			Env:     map[string]string{"url": byBase["REDIS_URL"]},
		}, nil
	case "kafka":
		d.NeedKafka = true

		prodBase, consBase := camel+"Producer", camel+"Consumer"
		if u.Service == "kafka" {
			prodBase, consBase = "producer", "consumer"
		}

		return appWire{
			Service: u.Service,
			Kind:    "kafka",
			Var:     claimVar(taken, prodBase),
			Var2:    claimVar(taken, consBase),
			Env: map[string]string{
				"brokers": byBase["KAFKA_BROKERS"],
				"proto":   byBase["KAFKA_SECURITY_PROTOCOL"],
			},
		}, nil
	case "s3-compatible":
		d.NeedS3 = true

		base := camel + "S3"
		if u.Service == "s3-compatible" {
			base = "s3Client"
		}

		return appWire{
			Service: u.Service,
			Kind:    "s3",
			Var:     claimVar(taken, base),
			Env: map[string]string{
				"endpoint": byBase["S3_ENDPOINT"],
				"region":   byBase["S3_REGION"],
				"key":      byBase["S3_ACCESS_KEY_ID"],
				"secret":   byBase["S3_SECRET_ACCESS_KEY"],
			},
		}, nil
	case "consul":
		d.NeedConsul = true

		base := camel + "Consul"
		if u.Service == "consul" {
			base = "consul"
		}

		return appWire{
			Service: u.Service,
			Kind:    "consul",
			Var:     claimVar(taken, base),
			Env: map[string]string{
				"addr": byBase["CONSUL_HTTP_ADDR"],
				"ca":   byBase["CONSUL_CACERT"],
			},
		}, nil
	case "otel":
		d.NeedOtel = true

		base := camel + "Shutdown"
		if u.Service == "otel" {
			base = "shutdownOtel"
		}

		return appWire{
			Service: u.Service,
			Kind:    "otel",
			Var:     claimVar(taken, base),
			Env: map[string]string{
				"endpoint": byBase["OTEL_EXPORTER_OTLP_ENDPOINT"],
				"traces":   byBase["OTEL_TRACES_EXPORTER"],
				"metrics":  byBase["OTEL_METRICS_EXPORTER"],
				"logs":     byBase["OTEL_LOGS_EXPORTER"],
			},
		}, nil
	case "haproxy", "envoy", "traefik", "nginx":
		d.NeedHTTP = true

		return appWire{
			Service: u.Service,
			Kind:    "http",
			Var:     claimVar(taken, camel+"URL"),
			Env:     map[string]string{"url": urlVar},
		}, nil
	default:
		return appWire{}, fmt.Errorf("no wiring template for %s (type %s)", u.Service, u.Type)
	}
}

// reservedAppVars lists identifiers the generated main.go already uses for
// packages, locals, helpers, or Go builtins so service variable names never
// shadow or collide with them.
func reservedAppVars() map[string]bool {
	names := []string{
		// Locals and helpers in main.go
		"ctx", "stop", "httpClient", "main", "init", "requireEnv",
		"newPostgres", "newRedis", "newKafkaProducer", "newKafkaConsumer",
		"newS3", "newConsul", "newOtel", "newHTTPClient",
		// Imported package identifiers
		"context", "errors", "tls", "fmt", "log", "slog", "http", "os", "signal", "strings",
		"syscall", "time", "aws", "config", "credentials", "s3", "consulapi",
		"pgxpool", "redis", "kadm", "kgo",
		"otelslog", "otel", "otlploggrpc", "otlpmetricgrpc", "otlptracegrpc",
		"otellogglobal", "propagation", "sdklog", "sdkmetric", "resource",
		"sdktrace", "semconv",
		// Common Go predeclared identifiers
		"any", "append", "bool", "byte", "cap", "clear", "close", "complex",
		"copy", "delete", "error", "false", "imag", "int", "len", "make",
		"max", "min", "new", "nil", "panic", "print", "println", "real",
		"recover", "rune", "string", "true",
	}

	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}

	return out
}

func claimVar(taken map[string]bool, base string) string {
	if base == "" || unicode.IsDigit(rune(base[0])) {
		base = "svc" + base
	}

	name := base
	for i := 2; taken[name] || token.IsKeyword(name); i++ {
		name = base + strconv.Itoa(i)
	}

	taken[name] = true

	return name
}

// camelIdentifier turns a DNS label like "my-cache-2" into "myCache2".
func camelIdentifier(s string) string {
	var b strings.Builder

	upperNext := false

	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			if b.Len() > 0 {
				upperNext = true
			}

			continue
		}

		if upperNext {
			b.WriteRune(unicode.ToUpper(r))

			upperNext = false
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}

	return b.String()
}
