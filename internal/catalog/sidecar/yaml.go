package sidecar

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// obj is a YAML mapping that keeps its keys in the order written, so
// generated configuration reads top-down like a hand-written file.
type obj []field

type field struct {
	key   string
	value any
}

// o builds an obj from alternating keys and values.
func o(kv ...any) obj {
	if len(kv)%2 != 0 {
		panic("sidecar: odd number of arguments to o")
	}

	out := make(obj, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, field{key: kv[i].(string), value: kv[i+1]})
	}

	return out
}

// list is a YAML sequence of any values.
type list = []any

func (m obj) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	for _, f := range m {
		var v yaml.Node
		if err := v.Encode(f.value); err != nil {
			return nil, fmt.Errorf("%s: %w", f.key, err)
		}

		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.key}, &v)
	}

	return n, nil
}

// encodeYAML renders a generated document with a comment header.
func encodeYAML(header string, doc any) ([]byte, error) {
	var buf bytes.Buffer
	for _, line := range strings.Split(header, "\n") {
		buf.WriteString("# " + line + "\n")
	}

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(doc); err != nil {
		return nil, err
	}

	if err := enc.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
