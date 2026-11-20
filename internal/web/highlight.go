package web

import (
	"go/scanner"
	"go/token"
	"html"
	"regexp"
	"strings"
)

// Token classes for highlighted Go. styles/input.css lists this file as a
// Tailwind source, so the class names must stay literal here.
const (
	goKeywordClass = "text-indigo-700"
	goStringClass  = "text-emerald-700"
	goCommentClass = "italic text-slate-400"
	goNumberClass  = "text-amber-700"
	goCallClass    = "font-medium text-slate-900"
)

// highlightGo returns code as HTML, one entry per line: the exact text,
// escaped, with keywords, string and rune literals, comments, numbers and
// calls (identifiers immediately followed by "(") wrapped in spans. It scans
// with go/scanner, so fragments that are not whole Go files work too: scan
// errors are ignored, and text the scanner does not account for is kept as
// plain text.
func highlightGo(code string) []string {
	src := []byte(code)
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))

	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)

	type segment struct{ class, text string }

	var segs []segment

	last := 0

	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}

		off := file.Offset(pos)

		var class string

		switch {
		case tok.IsKeyword():
			class, lit = goKeywordClass, tok.String()
		case tok == token.STRING || tok == token.CHAR:
			class = goStringClass
		case tok == token.COMMENT:
			class = goCommentClass
		case tok == token.INT || tok == token.FLOAT || tok == token.IMAG:
			class = goNumberClass
		case tok == token.IDENT:
			if end := off + len(lit); end < len(src) && src[end] == '(' {
				class = goCallClass
			}
		}
		// The scanner drops carriage returns from some literals; a token
		// that no longer matches the source stays plain text.
		if class == "" || lit == "" || off < last || !strings.HasPrefix(code[off:], lit) {
			continue
		}

		if off > last {
			segs = append(segs, segment{"", code[last:off]})
		}

		segs = append(segs, segment{class, lit})
		last = off + len(lit)
	}

	if last < len(code) {
		segs = append(segs, segment{"", code[last:]})
	}

	var (
		lines []string
		b     strings.Builder
	)

	for _, sg := range segs {
		for i, part := range strings.Split(sg.text, "\n") {
			if i > 0 {
				lines = append(lines, b.String())
				b.Reset()
			}

			switch {
			case part == "":
			case sg.class == "":
				b.WriteString(html.EscapeString(part))
			default:
				b.WriteString(`<span class="` + sg.class + `">` + html.EscapeString(part) + `</span>`)
			}
		}
	}

	return append(lines, b.String())
}

// isYAML reports whether a code block's file name is a YAML file. A note
// in parentheses after the name, as in "envoy.yaml (Envoy)", is ignored.
func isYAML(filename string) bool {
	name, _, _ := strings.Cut(filename, " (")

	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// highlightYAML returns code as HTML, one entry per line, like highlightGo:
// the exact text, escaped, with mapping keys, scalar values, comments, and
// the content of block scalars (| and >) wrapped in spans. It reads line by
// line and never fails: text it does not recognize is kept as plain text.
func highlightYAML(code string) []string {
	lines := strings.Split(code, "\n")
	out := make([]string, 0, len(lines))
	block := -1 // indentation of the key that opened a block scalar

	for _, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if block >= 0 {
			if strings.TrimSpace(line) == "" || indent > block {
				out = append(out, yamlSpan("", line[:indent])+yamlSpan(goStringClass, line[indent:]))

				continue
			}

			block = -1
		}

		var b strings.Builder

		b.WriteString(html.EscapeString(line[:indent]))
		rest := line[indent:]
		// Sequence dashes.
		for strings.HasPrefix(rest, "- ") || rest == "-" {
			n := min(2, len(rest))
			b.WriteString(html.EscapeString(rest[:n]))
			rest = rest[n:]
		}

		keyAt := len(line) - len(rest)
		if k := yamlKeyEnd(rest); k > 0 {
			b.WriteString(yamlSpan(goKeywordClass, rest[:k]))
			b.WriteString(":")

			rest = rest[k+1:]
		}

		value, comment := yamlSplitComment(rest)
		v := strings.TrimSpace(value)
		lead := value[:len(value)-len(strings.TrimLeft(value, " \t"))]
		trail := value[len(lead)+len(v):]

		b.WriteString(html.EscapeString(lead))
		b.WriteString(yamlSpan(yamlValueClass(v), v))
		b.WriteString(html.EscapeString(trail))
		b.WriteString(yamlSpan(goCommentClass, comment))

		if v != "" && (v[0] == '|' || v[0] == '>') && strings.Trim(v[1:], "+-0123456789") == "" {
			block = keyAt
		}

		out = append(out, b.String())
	}

	return out
}

// yamlKeyEnd returns the index of the colon that ends a mapping key at the
// start of s, or -1: a quoted key, or plain text with no "#", "{" or "[",
// followed by ": " or the end of the line.
func yamlKeyEnd(s string) int {
	end := -1

	switch {
	case s == "":
		return -1
	case s[0] == '"' || s[0] == '\'':
		if i := strings.IndexByte(s[1:], s[0]); i >= 0 {
			end = i + 2
		}
	default:
		end = strings.Index(s, ":")
		if end <= 0 || strings.ContainsAny(s[:end], "#{[,&*!|>\"'") {
			return -1
		}
	}

	if end <= 0 || end >= len(s) || s[end] != ':' {
		return -1
	}

	if end+1 < len(s) && s[end+1] != ' ' && s[end+1] != '\t' {
		return -1
	}

	return end
}

// yamlSplitComment splits s before a comment: a "#" at the start or after
// a space, outside quotes.
func yamlSplitComment(s string) (string, string) {
	var quote byte

	for i := range len(s) {
		c := s[i]

		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i], s[i:]
		}
	}

	return s, ""
}

// yamlValueClass picks the class of a scalar value: numbers, booleans and
// null as numbers; quoted and plain text as strings. Flow collections,
// anchors, aliases, tags and block indicators stay plain.
func yamlValueClass(v string) string {
	switch {
	case v == "":
		return ""
	case strings.ContainsAny(v[:1], "{[&*!|>"):
		return ""
	case yamlNumber.MatchString(v):
		return goNumberClass
	}

	switch strings.ToLower(v) {
	case "true", "false", "null", "~":
		return goNumberClass
	}

	return goStringClass
}

var yamlNumber = regexp.MustCompile(`^[-+]?(\d[\d_]*(\.\d*)?([eE][-+]?\d+)?|0x[0-9a-fA-F]+|0o[0-7]+|\.inf|\.nan)$`)

// yamlSpan wraps escaped text in a span of class, or returns it escaped
// when class or text is empty.
func yamlSpan(class, text string) string {
	if class == "" || text == "" {
		return html.EscapeString(text)
	}

	return `<span class="` + class + `">` + html.EscapeString(text) + `</span>`
}
