package starters

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/secrets"
	templates "github.com/brian-nunez/breadboard/starters"
)

// testUses builds the uses of an app the way bb up injects them: each
// service's contract in order, and a variable another service already set
// prefixed with the service's name.
func testUses(t *testing.T, services ...[3]string) []AppUse {
	t.Helper()

	taken := map[string]bool{}

	var uses []AppUse

	for _, s := range services {
		name, typ, plain := s[0], s[1], s[2] == "plain"

		e, ok := catalog.Lookup(typ)
		if !ok {
			t.Fatalf("no catalog type %s", typ)
		}

		cl := catalog.Client{Service: name, FQDN: name + ".bb.internal", Config: e.ResolveConfig(nil), Secret: func(string) string { return "s3cr3t" }}
		if e.Proxy != nil {
			for _, m := range e.Proxy.Modes {
				cl.Upstreams = append(cl.Upstreams, catalog.Upstream{Mode: m})
			}
		}

		if plain {
			cl.Plain = &catalog.Plain{FQDN: name + ".plain.bb.internal"}
		}

		u := AppUse{Service: name, Type: typ, Title: e.Title, Summary: e.Summary, Plaintext: plain}
		for _, v := range e.Client(cl) {
			n := v.Name
			if taken[n] {
				n = secrets.EnvPrefixed(name, n)
			}

			taken[n] = true

			val := v.Value
			if v.Secret {
				val = ""
			}

			u.Vars = append(u.Vars, AppVar{Name: n, Base: v.Name, Value: val, Secret: v.Secret})
		}

		uses = append(uses, u)
	}

	return uses
}

// allTypes is one service of every catalog type.
func allTypes() [][3]string {
	var out [][3]string
	for _, e := range catalog.All() {
		out = append(out, [3]string{e.ID, e.ID, ""})
	}

	return out
}

func appFiles(t *testing.T, a AppScaffold) map[string]string {
	t.Helper()

	files, err := renderApp(templates.FS, a)
	if err != nil {
		t.Fatal(err)
	}

	have := map[string]string{}
	for _, f := range files {
		have[f.Path] = string(f.Data)
	}

	return have
}

func TestEveryCatalogTypeHasAnSDK(t *testing.T) {
	for _, e := range catalog.All() {
		if _, ok := sdkImports[e.ID]; !ok {
			t.Errorf("catalog type %s has no client library for bb add app; add it to sdkImports", e.ID)
		}
	}

	for typ := range sdkImports {
		if _, ok := catalog.Lookup(typ); !ok {
			t.Errorf("sdkImports maps %s, which is not a catalog type", typ)
		}
	}
}

func TestEveryContractVariableIsDescribed(t *testing.T) {
	for _, e := range catalog.All() {
		for _, plain := range []bool{false, true} {
			for _, u := range testUses(t, [3]string{e.ID, e.ID, map[bool]string{true: "plain"}[plain]}) {
				for _, v := range u.Vars {
					if _, ok := EnvHelp(v.Base); !ok {
						t.Errorf("%s variable %s has no description in envHelp", e.ID, v.Base)
					}
				}
			}
		}
	}
}

// The scaffold's go.mod pins each client library at the version the
// starters pin, so what a learner writes matches the lessons.
func TestAppModulesMatchStarters(t *testing.T) {
	require := regexp.MustCompile(`(?m)^\s*(?:require\s+)?(\S+) (v\S+)`)
	pins := map[string]map[string]string{} // module -> version -> file

	err := fs.WalkDir(templates.FS, "go", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) != "go.mod.tmpl" || strings.HasPrefix(p, AppTemplate+"/") {
			return err
		}

		data, err := fs.ReadFile(templates.FS, p)
		if err != nil {
			return err
		}

		for _, m := range require.FindAllStringSubmatch(direct(string(data)), -1) {
			if pins[m[1]] == nil {
				pins[m[1]] = map[string]string{}
			}

			pins[m[1]][m[2]] = p
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	mod, err := fs.ReadFile(templates.FS, AppTemplate+"/go.mod.tmpl")
	if err != nil {
		t.Fatal(err)
	}

	have := map[string]string{}
	for _, m := range require.FindAllStringSubmatch(direct(string(mod)), -1) {
		have[m[1]] = m[2]
	}

	for module, version := range have {
		for v, file := range pins[module] {
			if v != version {
				t.Errorf("app go.mod pins %s %s, but %s pins %s", module, version, file, v)
			}
		}
	}

	for typ, imp := range sdkImports {
		if imp == "" {
			continue // an HTTP proxy: net/http
		}

		found := false

		for module := range have {
			if imp == module || strings.HasPrefix(imp, module+"/") {
				found = true
			}
		}

		if !found {
			t.Errorf("%s imports %s, which the app go.mod does not require", typ, imp)
		}
	}
}

// direct drops a go.mod's indirect requirements: the starters agree on
// their client libraries, not on every transitive version.
func direct(mod string) string {
	var keep []string

	for _, line := range strings.Split(mod, "\n") {
		if !strings.Contains(line, "// indirect") {
			keep = append(keep, line)
		}
	}

	return strings.Join(keep, "\n")
}

func TestRenderApp(t *testing.T) {
	a := AppScaffold{Name: "shop", Experiment: "lab", Uses: testUses(t, [3]string{"postgres", "postgres", ""}, [3]string{"redis", "redis", ""})}
	have := appFiles(t, a)
	want := []string{"Dockerfile", "README.md", "go.mod", "go.sum", "main.go"}

	var got []string
	for p := range have {
		got = append(got, p)
	}

	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}

	main := have["main.go"]
	for _, want := range []string{
		"// Command shop is your own program in the Breadboard lab lab.",
		"// postgres (Postgres): PostgreSQL relational database",
		"DATABASE_URL", "(secret)", "PGHOST         host name: postgres.bb.internal",
		"REDIS_URL", "bb creds", "SSL_CERT_FILE", "BB_FQDN", "TLS_CERT_FILE", "TLS_KEY_FILE",
		"import (\n\t\"fmt\"\n\t\"os\"\n\t\"strings\"\n\n\t// Official client libraries for the services above. The _ keeps each\n\t// in the build until you use it; drop the _ when you do.\n" +
			"\t_ \"github.com/jackc/pgx/v5/pgxpool\" // postgres\n\t_ \"github.com/redis/go-redis/v9\"    // redis\n)\n",
		"func init() {\n\trequireEnv(\n\t\t// postgres (Postgres)\n\t\t\"DATABASE_URL\", \"PGHOST\",",
		"\t\t// redis (Redis)\n\t\t\"REDIS_URL\",\n\t)\n}",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go lacks %q:\n%s", want, main)
		}
	}

	if !strings.HasSuffix(main, "\nfunc main() {\n}\n") {
		t.Errorf("main is not empty:\n%s", main)
	}

	if strings.Contains(main, "s3cr3t") {
		t.Error("main.go contains a secret value")
	}

	if !strings.HasPrefix(have["go.mod"], "module example.com/shop\n\ngo "+GoVersion+"\n") {
		t.Errorf("go.mod = %s", have["go.mod"])
	}

	df := have["Dockerfile"]
	for _, want := range []string{"FROM golang:" + GoImage + " AS dev", "FROM alpine:" + AlpineVersion + " AS prod", "go build -o /usr/local/bin/shop .", "USER app", "--mount=type=cache,target=/root/.cache/go-build", "LABEL maintainer="} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}

	readme := have["README.md"]
	for _, want := range []string{"# shop", "| `redis` | Redis | `REDIS_URL` (secret) |", "bb creds", "go mod tidy", "bb add kafka", "uses: [postgres, redis, kafka]", "bb learn kafka", "ports:", "host:", "checks the variables of the services in `uses`", "Breadboard supplies the app's own TLS and identity variables", "example.com/shop", "`main` is empty", "`bb up` shows that `shop` exited"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md lacks %q", want)
		}
	}

	for _, gone := range []string{"services.go", "must(", "uncomment", "connectPostgres"} {
		if strings.Contains(readme+main, gone) {
			t.Errorf("README.md or main.go still mentions %q", gone)
		}
	}
}

// The README's add-a-service example is always a valid edit: a type no
// service or app takes, or a generic one when every candidate is taken.
func TestAppReadmeExample(t *testing.T) {
	readme := appFiles(t, AppScaffold{Name: "app", Experiment: "lab", Uses: testUses(t, [3]string{"redis", "redis", ""}), Taken: []string{"redis", "postgres", "app"}})["README.md"]
	if !strings.Contains(readme, "bb add kafka ") || !strings.Contains(readme, "uses: [redis, kafka]") {
		t.Errorf("README example:\n%s", readme)
	}

	readme = appFiles(t, AppScaffold{Name: "app", Experiment: "lab", Taken: exampleTypes})["README.md"]
	if !strings.Contains(readme, "bb add <type> --name <new-name>") || strings.Contains(readme, "uses: [") {
		t.Errorf("README generic example:\n%s", readme)
	}
}

// One import per client package, with every service that uses it; HTTP
// proxies import nothing but appear in the header; prefixed variables are
// checked by their injected names.
func TestRenderAppImports(t *testing.T) {
	a := AppScaffold{Name: "app", Experiment: "lab", Uses: testUses(t,
		[3]string{"postgres", "postgres", ""}, [3]string{"analytics", "postgres", ""}, [3]string{"pool", "pgbouncer", ""},
		[3]string{"edge", "haproxy", ""})}

	main := appFiles(t, a)["main.go"]
	if strings.Count(main, `"github.com/jackc/pgx/v5/pgxpool"`) != 1 || !strings.Contains(main, `_ "github.com/jackc/pgx/v5/pgxpool" // postgres, analytics, pool`) {
		t.Errorf("pgxpool import:\n%s", main)
	}

	if strings.Contains(main, "net/http") || !strings.Contains(main, "// edge (HAProxy):") || !strings.Contains(main, `"EDGE_URL",`) {
		t.Errorf("HTTP proxy:\n%s", main)
	}

	if !strings.Contains(main, `"ANALYTICS_DATABASE_URL", "ANALYTICS_PGHOST"`) {
		t.Errorf("prefixed variables:\n%s", main)
	}
}

func TestRenderAppWithoutServices(t *testing.T) {
	main := appFiles(t, AppScaffold{Name: "solo", Experiment: "lab"})["main.go"]
	for _, want := range []string{"No services yet", "requireEnv()", "import (\n\t\"fmt\"\n\t\"os\"\n\t\"strings\"\n)\n", "\nfunc main() {\n}\n"} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go lacks %q:\n%s", want, main)
		}
	}

	if strings.Contains(main, "_ \"") {
		t.Errorf("an app without services has blank imports:\n%s", main)
	}
}

// TestRenderedAppBuilds renders the scaffold as bb add app writes it and
// runs go build and go vet on it, as a learner would, for every catalog
// type at once, each alone, no service, colliding service names, and app
// names that are standard library packages. It then runs the binary
// without its variables (it must exit 1 with one message naming every
// variable) and with them (the empty main exits 0 at once). It downloads
// modules, so -short skips it.
func TestRenderedAppBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads modules")
	}

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}

	cases := map[string][][3]string{
		"every-type": allTypes(),
		"none":       nil,
		"prefixed": {
			{"postgres", "postgres", ""},
			{"analytics", "postgres", ""},
			{"pool", "pgbouncer", ""},
			{"s3-compatible", "s3-compatible", ""},
			{"archive", "s3-compatible", ""},
			{"consul", "consul", ""},
			{"registry", "consul", "plain"},
			{"kafka", "kafka", "plain"},
			{"edge-1", "haproxy", ""},
		},
		// Distinct service names that read alike.
		"colliding": {{"a-b", "redis", ""}, {"a--b", "redis", ""}, {"a-b2", "redis", ""}},
		// App names that are standard library packages: the module path
		// is example.com/<name>, so they do not shadow them.
		"log":     nil,
		"context": {{"redis", "redis", ""}},
	}
	for _, e := range catalog.All() {
		cases["alone-"+e.ID] = [][3]string{{e.ID, e.ID, ""}}
	}

	for name, services := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			appName := "app-" + name
			if name == "log" || name == "context" {
				appName = name
			}

			a := AppScaffold{Name: appName, Experiment: "lab", Uses: testUses(t, services...)}

			dir := t.TempDir()
			for p, data := range appFiles(t, a) {
				if err := os.WriteFile(filepath.Join(dir, p), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			for _, args := range [][]string{{"build", "-o", "bin/app", "."}, {"vet", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir

				cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=go"+GoVersion, "CGO_ENABLED=0")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}

			checkRun(t, filepath.Join(dir, "bin", "app"), a)
		})
	}
}

// checkRun runs the built scaffold without its variables and with them.
func checkRun(t *testing.T, bin string, a AppScaffold) {
	t.Helper()

	var names []string

	for _, u := range a.Uses {
		for _, v := range u.Vars {
			names = append(names, v.Name)
		}
	}

	env := []string{"PATH=" + os.Getenv("PATH")}

	if len(names) > 0 {
		cmd := exec.Command(bin)
		cmd.Env = env

		out, err := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); err == nil || code != 1 {
			t.Fatalf("without its variables the app exited %d (%v), want 1:\n%s", code, err, out)
		}

		want := a.Name + " cannot start: missing environment variables: " + strings.Join(names, ", ") + "\n"
		if !strings.HasPrefix(string(out), want) || strings.Contains(string(out), "panic") {
			t.Fatalf("without its variables the app said:\n%s\nwant it to start with:\n%s", out, want)
		}
	}

	for _, n := range names {
		env = append(env, n+"=x")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)

	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("with its variables the empty program should exit 0 silently; got %v:\n%s", err, out)
	}
}
