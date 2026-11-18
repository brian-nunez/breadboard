package web

import (
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/starters"
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainText undoes highlightGo: tags stripped, entities unescaped, lines
// joined.
func plainText(lines []string) string {
	return html.UnescapeString(htmlTag.ReplaceAllString(strings.Join(lines, "\n"), ""))
}

// docsGoSnippets is every Go snippet the docs pass to docCode with a .go file
// name. TestDocsGoSnippetsListed keeps it complete.
var docsGoSnippets = map[string]string{
	"consulBlockingCode": consulBlockingCode, "consulCallCode": consulCallCode, "consulConnectCode": consulConnectCode,
	"consulGreeterCode": consulGreeterCode, "consulKVCode": consulKVCode, "consulLookupCode": consulLookupCode,
	"consulSelfDeregisterCode": consulSelfDeregisterCode, "consulSelfRegisterCode": consulSelfRegisterCode, "consulTTLCode": consulTTLCode,
	"gsPgxCode": gsPgxCode, "gsServeTLSCode": gsServeTLSCode,
	"kafkaConnectCode": kafkaConnectCode, "kafkaConsumeCode": kafkaConsumeCode, "kafkaCreateTopicCode": kafkaCreateTopicCode,
	"kafkaGroupCode": kafkaGroupCode, "kafkaIdempotentCode": kafkaIdempotentCode, "kafkaManualCommitCode": kafkaManualCommitCode,
	"kafkaProduceCode": kafkaProduceCode,
	"lbShutdownCode":   lbShutdownCode,
	"pgConnectCode":    pgConnectCode, "pgExecCode": pgExecCode, "pgPoolCode": pgPoolCode, "pgQueryCode": pgQueryCode,
	"pgQueryRowCode": pgQueryRowCode, "pgSeedCode": pgSeedCode, "pgSerializableCode": pgSerializableCode,
	"pgSinkTableCode": pgSinkTableCode, "pgSinkUpsertCode": pgSinkUpsertCode, "pgTxCode": pgTxCode,
	"redisConnectCode": redisConnectCode, "redisHashCode": redisHashCode, "redisInvalidateCode": redisInvalidateCode,
	"redisListCode": redisListCode, "redisReadCode": redisReadCode, "redisSetCode": redisSetCode,
	"redisSetGetCode": redisSetGetCode, "redisTTLCode": redisTTLCode, "redisWriterCode": redisWriterCode,
	"redisZSetCode": redisZSetCode,
	"s3BucketCode":  s3BucketCode, "s3ClientCode": s3ClientCode, "s3EndpointEnvCode": s3EndpointEnvCode,
	"s3ListCode": s3ListCode, "s3MultipartCode": s3MultipartCode, "s3PresignCode": s3PresignCode, "s3PutCode": s3PutCode,
	"sidecarsAdapterCode": sidecarsAdapterCode, "sidecarsAmbassadorCode": sidecarsAmbassadorCode,
	"sidecarsListenCode": sidecarsListenCode, "sidecarsPoolCode": sidecarsPoolCode, "sidecarsTransparentCode": sidecarsTransparentCode,
}

func TestDocsGoSnippetsListed(t *testing.T) {
	call := regexp.MustCompile(`@docCode\("[^"]*\.go", (\w+)\)`)

	files, err := filepath.Glob("*.templ")
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		for _, m := range call.FindAllStringSubmatch(string(b), -1) {
			if _, ok := docsGoSnippets[m[1]]; !ok {
				t.Errorf("%s: Go snippet %s is not in docsGoSnippets", f, m[1])
			}
		}
	}
}

func TestHighlightGoKeepsText(t *testing.T) {
	inputs := map[string]string{
		"empty":               "",
		"newlines only":       "\n\n",
		"unterminated string": "s := \"never closed\nx := 1",
		"unterminated raw":    "s := `open\nand on",
		"unterminated rune":   "r := 'a",
		"unterminated block":  "/* still open\nfunc f() {}",
		"raw string":          "q := `line one\n\t\"two\" 'x'\n</pre>`\nf(q)",
		"pre in comment":      "// </pre><script>alert(1)</script>\n/* </div> & </pre> */\nx := 1",
		"less and amp":        "if a < b && c&d != 0 { x <- y; _ = &z; s := \"<&>\" }",
		"carriage returns":    "a := `x\r\ny`\r\n// c\r\n/* d\r\n*/ b := 1\r\n",
		"illegal chars":       "x := 1 @ # $ ? \x00 ¿ é",
		"not go":              "services:\n  web:\n    image: nginx:1.27 # pinned\n",
		"tabs and blanks":     "\tfunc f() {\n\n\t\treturn  1.5e3 + 0x1F + 2i\n\t}\n  ",
	}
	for name, code := range docsGoSnippets {
		inputs["docs "+name] = strings.Trim(code, "\n")
	}

	err := fs.WalkDir(starters.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go.tmpl") {
			return err
		}

		b, err := fs.ReadFile(starters.FS, path)
		inputs["starter "+path] = string(b)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, in := range inputs {
		lines := highlightGo(in)
		if got := plainText(lines); got != in {
			t.Errorf("%s: text changed\n got %q\nwant %q", name, got, in)
		}

		if want := strings.Count(in, "\n") + 1; len(lines) != want {
			t.Errorf("%s: %d lines, want %d", name, len(lines), want)
		}
	}
}

func TestHighlightGoClasses(t *testing.T) {
	got := strings.Join(highlightGo("func main() {\n\t// hi\n\tfmt.Println(\"a\", 'b', 42)\n}"), "\n")
	for _, want := range []string{
		`<span class="text-indigo-700">func</span>`,
		`<span class="font-medium text-slate-900">main</span>(`,
		`<span class="italic text-slate-400">// hi</span>`,
		`fmt.<span class="font-medium text-slate-900">Println</span>(`,
		`<span class="text-emerald-700">&#34;a&#34;</span>`,
		`<span class="text-emerald-700">&#39;b&#39;</span>`,
		`<span class="text-amber-700">42</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	// A token across lines is closed and reopened on each line.
	raw := highlightGo("x := `a\nb`")
	if raw[0] != "x := <span class=\"text-emerald-700\">`a</span>" || raw[1] != "<span class=\"text-emerald-700\">b`</span>" {
		t.Errorf("raw string lines = %q", raw)
	}
}

// codeBlocks returns each docCode block on a page, by file name.
func codeBlocks(body string) map[string][]string {
	block := regexp.MustCompile(`(?s)<span class="font-mono text-\[13px\] text-slate-600">([^<]*)</span>.*?data-copy-source>(.*?)</pre>`)

	out := map[string][]string{}
	for _, m := range block.FindAllStringSubmatch(body, -1) {
		out[m[1]] = append(out[m[1]], m[2])
	}

	return out
}

func TestDocsHighlightGoAndYAMLBlocks(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}

	for _, path := range []string{"/docs/kafka/consumer-groups", "/docs/sidecars/transparent-proxy", "/docs/envoy/config-anatomy"} {
		var goBlocks, yamlBlocks int

		for name, blocks := range codeBlocks(s.get(t, path, cookie).Body.String()) {
			for _, b := range blocks {
				switch {
				case strings.HasSuffix(name, ".go"):
					goBlocks++

					if !strings.Contains(b, `<span class="font-medium text-slate-900">`) || !strings.Contains(b, `<span class="text-emerald-700">`) {
						t.Errorf("%s: Go block %s is not highlighted", path, name)
					}
				case isYAML(name):
					yamlBlocks++

					if !strings.Contains(b, `<span class="text-indigo-700">`) {
						t.Errorf("%s: YAML block %s has no highlighted keys", path, name)
					}
				default:
					if strings.Contains(b, "<span") {
						t.Errorf("%s: %s block has highlight spans", path, name)
					}
				}
			}
		}

		if path == "/docs/kafka/consumer-groups" && goBlocks == 0 {
			t.Errorf("%s: no Go block", path)
		}

		if path != "/docs/kafka/consumer-groups" && yamlBlocks == 0 {
			t.Errorf("%s: no YAML block", path)
		}
	}
}

func TestNonGoBlockUnchanged(t *testing.T) {
	var b strings.Builder

	code := "server {\n  # a comment\n  listen 8443 ssl;\n"
	if err := docCode("nginx.conf", code).Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}

	want := `<div class="min-h-6 whitespace-pre">server {</div><div class="min-h-6 whitespace-pre text-slate-400">  # a comment</div><div class="min-h-6 whitespace-pre">  listen 8443 ssl;</div></pre>`
	if !strings.Contains(b.String(), want) {
		t.Errorf("nginx block = %s", b.String())
	}
}

func TestIsYAML(t *testing.T) {
	for name, want := range map[string]bool{
		"experiment.yaml": true, "dynamic.yml": true, "envoy.yaml (Envoy)": true,
		".bb/runtime/docker-compose.yml": true, "Added to experiment.yaml": true,
		"main.go": false, "nginx.conf": false, "yaml": false, "envoy.yaml.tmpl": false,
	} {
		if got := isYAML(name); got != want {
			t.Errorf("isYAML(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHighlightYAMLKeepsText(t *testing.T) {
	inputs := map[string]string{
		"empty":            "",
		"newlines only":    "\n\n",
		"comment only":     "# a comment\n  # indented",
		"html":             "a: \"</pre><script>alert(1)</script>\"\nb: '<&>' # </div>",
		"quoted keys":      "'@type': x\n\"a: b\": c\n- 'a': 1",
		"unterminated":     "a: \"never closed\nb: 'open",
		"flow":             "a: {b: 1, c: [2, 3]}\nd: [x, 'y']",
		"block scalars":    "a: |\n  line one\n\n  line: two\nb: >-\n  folded\nc: 1",
		"dashes":           "- - a\n-\n- b: 1\n  c: 2\n---\n...",
		"anchors and tags": "base: &base\n  a: 1\nother: *base\nt: !!str 5",
		"hash in values":   "url: http://x/#frag\ncolor: '#fff' # real\nk: a#b",
		"colons in values": "image: nginx:1.27\ntime: 12:30:00\nurl: https://a:8443/x",
		"carriage returns": "a: 1\r\nb: x\r\n",
		"tabs":             "\ta: 1\n\t- b",
		"not yaml":         "func main() {\n\tfmt.Println(\"hi\")\n}",
	}

	err := fs.WalkDir(starters.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isYAML(strings.TrimSuffix(path, ".tmpl")) {
			return err
		}

		b, err := fs.ReadFile(starters.FS, path)
		inputs["starter "+path] = string(b)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, in := range inputs {
		lines := highlightYAML(in)
		if got := plainText(lines); got != in {
			t.Errorf("%s: text changed\n got %q\nwant %q", name, got, in)
		}

		if want := strings.Count(in, "\n") + 1; len(lines) != want {
			t.Errorf("%s: %d lines, want %d", name, len(lines), want)
		}
	}
}

func TestHighlightYAMLClasses(t *testing.T) {
	const (
		key = `<span class="text-indigo-700">`
		str = `<span class="text-emerald-700">`
		num = `<span class="text-amber-700">`
		com = `<span class="italic text-slate-400">`
	)

	in := "# top\nservices:\n  web:\n    image: nginx:1.27 # pinned\n    replicas: 3\n    tls: true\n    note: |\n      a: b\n    ports:\n      - '8443'\n      - name: x\n    deps: [a, b]"
	want := []string{
		com + `# top</span>`,
		key + `services</span>:`,
		`  ` + key + `web</span>:`,
		`    ` + key + `image</span>: ` + str + `nginx:1.27</span> ` + com + `# pinned</span>`,
		`    ` + key + `replicas</span>: ` + num + `3</span>`,
		`    ` + key + `tls</span>: ` + num + `true</span>`,
		`    ` + key + `note</span>: |`,
		`      ` + str + `a: b</span>`,
		`    ` + key + `ports</span>:`,
		`      - ` + str + `&#39;8443&#39;</span>`,
		`      - ` + key + `name</span>: ` + str + `x</span>`,
		`    ` + key + `deps</span>: [a, b]`,
	}

	got := highlightYAML(in)
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d\n got %s\nwant %s", i+1, got[i], want[i])
		}
	}
}
