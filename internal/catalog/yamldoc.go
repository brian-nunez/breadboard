package catalog

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ymap is a YAML mapping that keeps its keys in the order written and may
// carry comments, so a generated configuration reads top-down like a
// hand-written file that learners can copy. (internal/catalog/sidecar has
// its own builder; it imports this package, so it cannot be shared here.)
type ymap []yfield

type yfield struct {
	key     string
	value   any
	comment string
}

// yseq is a YAML sequence of any values.
type yseq = []any

// ycomment, placed before a key in ym's arguments, becomes that key's
// comment line(s).
type ycomment string

// ym builds a ymap from alternating keys and values; a ycomment before a
// key comments it.
func ym(kv ...any) ymap {
	out := make(ymap, 0, len(kv)/2)

	var pending ycomment

	for len(kv) > 0 {
		if c, ok := kv[0].(ycomment); ok {
			pending = c
			kv = kv[1:]

			continue
		}

		if len(kv) < 2 {
			panic("catalog: ym: a key without a value")
		}

		out = append(out, yfield{key: kv[0].(string), value: kv[1], comment: string(pending)})
		pending = ""
		kv = kv[2:]
	}

	return out
}

func (m ymap) MarshalYAML() (any, error) { return yamlNode(m) }

// yamlNode builds the node tree itself for ymap and yseq values: comments
// on nodes that a nested MarshalYAML returns are lost on encoding.
func yamlNode(value any) (*yaml.Node, error) {
	switch v := value.(type) {
	case ymap:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

		for _, f := range v {
			val, err := yamlNode(f.value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.key, err)
			}

			k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.key}
			if f.comment != "" {
				c := yamlComment(f.comment)
				if len(n.Content) == 0 {
					// The encoder drops a comment on the first key of a
					// mapping inside a sequence; on the mapping it shows.
					n.HeadComment = c
				} else {
					k.HeadComment = c
				}
			}

			n.Content = append(n.Content, k, val)
		}

		return n, nil
	case yseq:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

		for _, item := range v {
			c, err := yamlNode(item)
			if err != nil {
				return nil, err
			}

			n.Content = append(n.Content, c)
		}

		return n, nil
	}

	n := &yaml.Node{}
	if err := n.Encode(value); err != nil {
		return nil, err
	}

	return n, nil
}

// encodeYAMLDoc renders a generated document under a comment header. The
// documents are built from strings, numbers and booleans, so encoding
// cannot fail; a failure is a programming error.
func encodeYAMLDoc(header []string, doc any) []byte {
	var buf bytes.Buffer
	for _, line := range header {
		buf.WriteString(yamlComment(line) + "\n")
	}

	buf.WriteString("\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(doc); err != nil {
		panic(fmt.Sprintf("catalog: encode generated YAML: %v", err))
	}

	if err := enc.Close(); err != nil {
		panic(fmt.Sprintf("catalog: encode generated YAML: %v", err))
	}

	return buf.Bytes()
}

// yamlComment turns text into YAML comment lines through Comment. Text
// may come from experiment.yaml (an upstream's reason). On top of what
// Comment does, NEL, LS and PS, which a YAML parser also honours as line
// breaks, start a new comment line, and a byte order mark becomes a space:
// nothing in the text can end the comment.
func yamlComment(text string) string {
	text = strings.NewReplacer("\u0085", "\n", "\u2028", "\n", "\u2029", "\n", "\ufeff", " ").Replace(text)

	return Comment("# ", text)
}
