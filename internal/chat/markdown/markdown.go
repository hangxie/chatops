// Package markdown splits model replies into prose and GitHub-flavored Markdown tables for chat backends.
package markdown

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Fence opens and closes a code block.
const Fence = "```"

var (
	tableSep  = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	cellMarks = regexp.MustCompile("\\*\\*|__|`")
)

// Segment is either a run of prose or a Markdown table's cells, header row first.
type Segment struct {
	Text  string
	Table [][]string
}

// Render rewrites prose lines with inline (nil keeps them) and renders tables as aligned code blocks.
func Render(text string, inline func(string) string) string {
	segs := Parse(text, inline)
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.Text
		if s.Table != nil {
			parts[i] = CodeBlock(s.Table)
		}
	}
	return strings.Join(parts, "\n")
}

// Parse splits text into prose and tables, rewriting prose lines outside code fences with inline (nil keeps them).
func Parse(text string, inline func(string) string) []Segment {
	if inline == nil {
		inline = func(line string) string { return line }
	}
	lines := strings.Split(text, "\n")
	var segs []Segment
	var prose []string
	flush := func() {
		if prose != nil {
			segs = append(segs, Segment{Text: strings.Join(prose, "\n")})
			prose = nil
		}
	}
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case IsFence(line):
			inFence = !inFence
			prose = append(prose, line)
		case inFence:
			prose = append(prose, line)
		case tableLen(lines[i:]) > 0:
			n := tableLen(lines[i:])
			flush()
			segs = append(segs, Segment{Table: tableRows(lines[i : i+n])})
			i += n - 1
		default:
			prose = append(prose, inline(line))
		}
	}
	flush()
	return segs
}

// IsFence reports whether line opens or closes a code block.
func IsFence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), Fence)
}

// tableLen returns how many lines starting at lines[0] form a Markdown table, or 0 if none does.
func tableLen(lines []string) int {
	if len(lines) < 2 || !strings.Contains(lines[0], "|") ||
		!strings.Contains(lines[1], "|") || !tableSep.MatchString(lines[1]) {
		return 0
	}
	framed := strings.HasPrefix(strings.TrimSpace(lines[0]), "|")
	cols := len(tableCells(lines[0]))
	n := 2
	for n < len(lines) && tableRow(lines[n], framed, cols) {
		n++
	}
	return n
}

// tableRow reports whether line continues a table: it must match the header's column count, and start with a
// pipe when the header does, so prose or a malformed row ends the table.
func tableRow(line string, framed bool, cols int) bool {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") || IsFence(line) || framed && !strings.HasPrefix(line, "|") {
		return false
	}
	return len(tableCells(line)) == cols
}

// tableRows returns the cells of each table line, dropping the separator line.
func tableRows(lines []string) [][]string {
	rows := make([][]string, 0, len(lines)-1)
	for i, line := range lines {
		if i != 1 {
			rows = append(rows, tableCells(line))
		}
	}
	return rows
}

func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i, cell := range cells {
		cells[i] = strings.TrimSpace(cellMarks.ReplaceAllString(cell, ""))
	}
	return cells
}

// CodeBlock renders rows as space-aligned columns in a code block, which chat clients show in monospace.
func CodeBlock(rows [][]string) string {
	var widths []int
	for _, cells := range rows {
		for len(widths) < len(cells) {
			widths = append(widths, 0)
		}
		for c, cell := range cells {
			widths[c] = max(widths[c], utf8.RuneCountInString(cell))
		}
	}
	out := make([]string, 0, len(rows)+2)
	out = append(out, Fence)
	for _, cells := range rows {
		var b strings.Builder
		for c, cell := range cells {
			b.WriteString(cell)
			if c < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[c]-utf8.RuneCountInString(cell)+2))
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(append(out, Fence), "\n")
}
