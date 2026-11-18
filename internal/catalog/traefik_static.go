package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// traefikOwnedStatic are the top-level static configuration keys a
// static_file must not set, with the reason the error gives. Traefik
// matches option names without regard to case, so the check does too.
var traefikOwnedStatic = []struct{ key, why string }{
	{"entryPoints", "Breadboard's listeners (websecure, passthrough and the loopback traefik entry point) and their TLS and request checks"},
	{"providers", "the file provider loads Breadboard's certificate and the routes; change routes with file"},
	{"api", "the API and dashboard on the loopback traefik entry point, which bb explore reads"},
	{"ping", "the health check asks /ping on the traefik entry point"},
	{"global", "the version check and anonymous usage statistics stay off, so a lab sends nothing out"},
}

// traefikReplacedStatic are the keys a static_file may set in place of
// Breadboard's own.
var traefikReplacedStatic = []string{"log", "accessLog"}

// traefikMergeStatic renders traefik.yml with the service's static_file
// merged in (D-207): a YAML mapping of static configuration sections.
// Keys Breadboard owns are refused; log and accessLog replace
// Breadboard's; every other section is added as given, after Breadboard's
// own, which keep their comments.
func traefikMergeStatic(s Server) ([]byte, error) {
	user, err := parseStaticFile(s.UserStatic.Data)
	if err != nil {
		return nil, err
	}

	var errs []error

	seen := map[string]string{}

	for i := 0; i+1 < len(user.Content); i += 2 {
		k := user.Content[i]
		// Only a string key is compared by its text: a tagged key
		// (!!binary ZW50cnlQb2ludHM= decodes to entryPoints), a merge key
		// or an alias could name a section Breadboard owns under other
		// text, and an anchor on a key could be referenced out of order.
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || k.Anchor != "" {
			errs = append(errs, fmt.Errorf("line %d: top-level keys must be plain section names, without a non-string tag, anchor, alias or merge key (<<), which could set a section Breadboard owns", k.Line))

			continue
		}

		low := strings.ToLower(k.Value)
		if prev, dup := seen[low]; dup {
			errs = append(errs, fmt.Errorf("line %d: %s repeats %s (Traefik ignores case in option names)", k.Line, k.Value, prev))

			continue
		}

		seen[low] = k.Value
		for _, o := range traefikOwnedStatic {
			if strings.EqualFold(k.Value, o.key) {
				errs = append(errs, fmt.Errorf("line %d: %s is Breadboard's and cannot be set here: %s", k.Line, k.Value, o.why))
			}
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	// Sections move: a replaced log or accessLog takes Breadboard's place,
	// others go after Breadboard's. An alias whose anchor would then come
	// after it makes YAML Traefik cannot load, so every alias is expanded
	// into a copy of what it names first.
	budget := staticNodeBudget
	for i := 1; i < len(user.Content); i += 2 {
		v, err := expandAliases(user.Content[i], &budget)
		if err != nil {
			return nil, err
		}

		user.Content[i] = v
	}

	doc, err := yamlNode(traefikStaticDoc(s, "this service's certificate"))
	if err != nil {
		return nil, err
	}

	added := false

	for i := 0; i+1 < len(user.Content); i += 2 {
		k, v := user.Content[i], user.Content[i+1]
		if j := mappingIndex(doc, k.Value); j >= 0 && isReplaced(k.Value) {
			own := doc.Content[j]
			own.HeadComment = joinComments(yamlComment(fmt.Sprintf("%s from %s, replacing Breadboard's.\nbb logs %s shows only what goes to stdout.", own.Value, s.UserStatic.Path, s.Name)), k.HeadComment)
			own.LineComment, own.FootComment = k.LineComment, k.FootComment
			doc.Content[j+1] = v

			continue
		}

		if !added {
			k.HeadComment = joinComments(yamlComment(fmt.Sprintf("Added from %s (services.%s.static_file), as given.", s.UserStatic.Path, s.Name)), k.HeadComment)
			added = true
		}

		doc.Content = append(doc.Content, k, v)
	}

	return encodeYAMLDoc(traefikStaticHeader(s), doc), nil
}

// parseStaticFile reads a static_file: one YAML document holding a
// mapping.
func parseStaticFile(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("is empty; it must be a YAML mapping of static configuration sections")
		}

		return nil, fmt.Errorf("is not valid YAML: %w", err)
	}

	var more yaml.Node
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, errors.New("must hold one YAML document")
	}

	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("must be a YAML mapping of static configuration sections (tracing, metrics, ...)")
	}

	return doc.Content[0], nil
}

// mappingIndex is the index of key's key node in mapping m, matched
// without regard to case; -1 when absent.
func mappingIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if strings.EqualFold(m.Content[i].Value, key) {
			return i
		}
	}

	return -1
}

func isReplaced(key string) bool {
	for _, r := range traefikReplacedStatic {
		if strings.EqualFold(r, key) {
			return true
		}
	}

	return false
}

// joinComments puts Breadboard's comment above the user's own.
func joinComments(ours, theirs string) string {
	if theirs == "" {
		return ours
	}

	return ours + "\n" + theirs
}

// staticNodeBudget bounds the nodes alias expansion may create, so a
// small file of nested aliases cannot grow without limit.
const staticNodeBudget = 100000

// expandAliases returns a copy of n with every alias replaced by a copy of
// the node it names and every anchor removed.
func expandAliases(n *yaml.Node, budget *int) (*yaml.Node, error) {
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return nil, fmt.Errorf("line %d: alias *%s names no anchor", n.Line, n.Value)
		}

		cp, err := expandAliases(n.Alias, budget)
		if err != nil {
			return nil, err
		}

		cp.HeadComment, cp.LineComment, cp.FootComment = n.HeadComment, n.LineComment, n.FootComment

		return cp, nil
	}

	*budget--
	if *budget < 0 {
		return nil, fmt.Errorf("line %d: aliases expand to more than %d nodes", n.Line, staticNodeBudget)
	}

	cp := *n
	cp.Anchor = ""

	cp.Content = make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		e, err := expandAliases(c, budget)
		if err != nil {
			return nil, err
		}

		cp.Content[i] = e
	}

	return &cp, nil
}
