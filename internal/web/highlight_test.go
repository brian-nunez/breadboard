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

func TestDocsHighlightOnlyGoBlocks(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	for _, path := range []string{"/docs/kafka/consumer-groups", "/docs/sidecars/transparent-proxy"} {
		var goBlocks, otherBlocks int
		for name, blocks := range codeBlocks(s.get(t, path, cookie).Body.String()) {
			for _, b := range blocks {
				highlighted := strings.Contains(b, `<span class="font-medium text-slate-900">`) && strings.Contains(b, `<span class="text-emerald-700">`)
				switch {
				case strings.HasSuffix(name, ".go"):
					goBlocks++
					if !highlighted {
						t.Errorf("%s: Go block %s is not highlighted", path, name)
					}
				default:
					otherBlocks++
					if strings.Contains(b, "<span") {
						t.Errorf("%s: %s block has highlight spans", path, name)
					}
				}
			}
		}
		if goBlocks == 0 {
			t.Errorf("%s: no Go block", path)
		}
		if path == "/docs/sidecars/transparent-proxy" && otherBlocks == 0 {
			t.Errorf("%s: no yaml block", path)
		}
	}
}

func TestNonGoBlockUnchanged(t *testing.T) {
	var b strings.Builder
	code := "services:\n  # a comment\n  web: {image: \"nginx\"}\n"
	if err := docCode("experiment.yaml", code).Render(t.Context(), &b); err != nil {
		t.Fatal(err)
	}
	want := `<div class="min-h-6 whitespace-pre">services:</div><div class="min-h-6 whitespace-pre text-slate-400">  # a comment</div><div class="min-h-6 whitespace-pre">  web: {image: &#34;nginx&#34;}</div></pre>`
	if !strings.Contains(b.String(), want) {
		t.Errorf("yaml block = %s", b.String())
	}
}
