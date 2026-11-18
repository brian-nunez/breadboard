package web

import (
	"go/scanner"
	"go/token"
	"html"
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

	var lines []string
	var b strings.Builder
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
