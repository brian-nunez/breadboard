package web

import "strings"

// Learning notes are markdown shared with bb learn. The UI renders the small
// subset they use (## headings, paragraphs, "- " bullets, `code`, **bold**)
// into typed blocks, so nothing in a note is ever emitted as raw HTML.

type notes struct {
	Title    string
	Sections []noteSection
}

type noteSection struct {
	Heading string
	Blocks  []noteBlock
}

type noteBlock struct {
	List  bool
	Items [][]inline // one entry for a paragraph, one per bullet for a list
}

type inline struct {
	Text string
	Code bool
	Bold bool
}

func parseNotes(src string) notes {
	var n notes
	var sec *noteSection
	var para []string
	var list *noteBlock
	flushPara := func() {
		if len(para) > 0 && sec != nil {
			sec.Blocks = append(sec.Blocks, noteBlock{Items: [][]inline{inlines(strings.Join(para, " "))}})
		}
		para = nil
	}
	flushList := func() {
		if list != nil && sec != nil {
			sec.Blocks = append(sec.Blocks, *list)
		}
		list = nil
	}
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "# "):
			n.Title = strings.TrimPrefix(t, "# ")
		case strings.HasPrefix(t, "## "):
			flushPara()
			flushList()
			n.Sections = append(n.Sections, noteSection{Heading: strings.TrimPrefix(t, "## ")})
			sec = &n.Sections[len(n.Sections)-1]
		case strings.HasPrefix(t, "- "):
			flushPara()
			if list == nil {
				list = &noteBlock{List: true}
			}
			list.Items = append(list.Items, inlines(strings.TrimPrefix(t, "- ")))
		case t == "":
			flushPara()
			flushList()
		default:
			flushList()
			para = append(para, t)
		}
		if sec == nil && len(n.Sections) == 0 && t != "" && !strings.HasPrefix(t, "# ") {
			n.Sections = append(n.Sections, noteSection{})
			sec = &n.Sections[0]
		}
	}
	flushPara()
	flushList()
	return n
}

// section returns the named section, or an empty one.
func (n notes) section(heading string) noteSection {
	for _, s := range n.Sections {
		if strings.EqualFold(s.Heading, heading) {
			return s
		}
	}
	return noteSection{}
}

// bullets flattens a section's list items.
func (s noteSection) bullets() [][]inline {
	var out [][]inline
	for _, b := range s.Blocks {
		if b.List {
			out = append(out, b.Items...)
		}
	}
	return out
}

// inlines splits text on `code` and **bold** markers.
func inlines(s string) []inline {
	var out []inline
	for s != "" {
		i := strings.IndexAny(s, "`*")
		if i < 0 {
			out = append(out, inline{Text: s})
			break
		}
		switch {
		case s[i] == '`':
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				out = append(out, inline{Text: s})
				return out
			}
			if i > 0 {
				out = append(out, inline{Text: s[:i]})
			}
			out = append(out, inline{Text: s[i+1 : i+1+end], Code: true})
			s = s[i+2+end:]
		case strings.HasPrefix(s[i:], "**"):
			end := strings.Index(s[i+2:], "**")
			if end < 0 {
				out = append(out, inline{Text: s})
				return out
			}
			if i > 0 {
				out = append(out, inline{Text: s[:i]})
			}
			out = append(out, inline{Text: s[i+2 : i+2+end], Bold: true})
			s = s[i+4+end:]
		default:
			out = append(out, inline{Text: s[:i+1]})
			s = s[i+1:]
		}
	}
	return out
}
