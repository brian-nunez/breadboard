// Package designdocs holds tests that keep the design documents
// rendering: GitHub ends a Markdown table at the first blank line or at a
// row with the wrong number of cells, silently turning the rest into
// text.
package designdocs

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docs are the Markdown files checked, relative to the repository root.
var docs = []string{"README.md", "AGENTS.md", "docs/design/*.md", "docs/adr/*.md"}

// cells counts a table row's cells: pipes outside inline code, minus the
// leading and trailing ones. Escaped pipes (\|) are text.
func cells(row string) int {
	row = regexp.MustCompile("`[^`]*`").ReplaceAllString(row, "")
	row = strings.ReplaceAll(row, `\|`, "")
	return strings.Count(strings.TrimSpace(row), "|") - 1
}

var delimiter = regexp.MustCompile(`^\|(\s*:?-+:?\s*\|)+\s*$`)

// tables returns each table's rows: a header line, then a delimiter line,
// then every following line that starts with "|".
func tables(lines []string) [][]int {
	var out [][]int
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "|") || !delimiter.MatchString(strings.TrimSpace(lines[i+1])) {
			continue
		}
		rows := []int{i, i + 1}
		j := i + 2
		for ; j < len(lines) && strings.HasPrefix(lines[j], "|"); j++ {
			rows = append(rows, j)
		}
		out = append(out, rows)
		i = j
	}
	return out
}

func readDocs(t *testing.T) map[string][]string {
	t.Helper()
	files := map[string][]string{}
	for _, pattern := range docs {
		matches, err := filepath.Glob(filepath.Join("..", "..", pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			files[m] = strings.Split(string(data), "\n")
		}
	}
	if len(files) == 0 {
		t.Fatal("no Markdown files found")
	}
	return files
}

// Every row of every table has the header's number of cells.
func TestTableRowsMatchTheirHeader(t *testing.T) {
	for name, lines := range readDocs(t) {
		for _, rows := range tables(lines) {
			want := cells(lines[rows[0]])
			for _, r := range rows[2:] {
				if got := cells(lines[r]); got != want {
					t.Errorf("%s:%d: %d cells, the header has %d: %.80s", name, r+1, got, want, lines[r])
				}
			}
		}
	}
}

// A table row after a blank line starts a broken table: GitHub ends the
// table at the blank line and prints the rest as text.
func TestNoRowsAfterABlankLine(t *testing.T) {
	for name, lines := range readDocs(t) {
		inTable := false
		for i, l := range lines {
			switch {
			case i+1 < len(lines) && strings.HasPrefix(l, "|") && delimiter.MatchString(strings.TrimSpace(lines[i+1])):
				inTable = true
			case strings.HasPrefix(l, "|"):
				if !inTable {
					t.Errorf("%s:%d: table row outside a table (a blank line or text above it): %.80s", name, i+1, l)
				}
			default:
				inTable = false
			}
		}
	}
}

var decisionRow = regexp.MustCompile(`^\| D-(\d{3}) \|`)

// The decision log is one table whose ids rise by one, with no duplicates
// or gaps other than ids recorded as skipped.
func TestDecisionLogIsOneOrderedTable(t *testing.T) {
	lines := readDocs(t)[filepath.Join("..", "..", "docs", "design", "decisions.md")]
	var tableRows []int
	for _, rows := range tables(lines) {
		if strings.HasPrefix(lines[rows[0]], "| Id | Date") {
			tableRows = rows
			break
		}
	}
	if tableRows == nil {
		t.Fatal("decisions.md has no decision table")
	}
	inTable := map[int]bool{}
	prev := 0
	for _, r := range tableRows[2:] {
		inTable[r] = true
		m := decisionRow.FindStringSubmatch(lines[r])
		if m == nil {
			t.Errorf("decisions.md:%d: not a decision row: %.80s", r+1, lines[r])
			continue
		}
		id, _ := strconv.Atoi(m[1])
		if id <= prev {
			t.Errorf("decisions.md:%d: D-%03d after D-%03d", r+1, id, prev)
		}
		prev = id
	}
	for i, l := range lines {
		if decisionRow.MatchString(l) && !inTable[i] {
			t.Errorf("decisions.md:%d: decision row outside the decision table: %.80s", i+1, l)
		}
	}
}
