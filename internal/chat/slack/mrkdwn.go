package slack

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Slack renders *bold* and bullets but not the GitHub-flavored Markdown the model tends to
// emit (**bold**, [text](url), # headings, tables), so those show up raw. These patterns convert the
// common cases; links become plain "text (url)" since replies are posted escaped and link-free.
var (
	mdBoldStars = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdBoldUnder = regexp.MustCompile(`__(.+?)__`)
	mdLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdHeading   = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.*?)[ \t]*$`)
	mdBullet    = regexp.MustCompile(`(?m)^([ \t]*)[-*][ \t]+`)
	mdTableSep  = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	mdCellMarks = regexp.MustCompile("\\*\\*|__|`")
)

const codeFence = "```"

// segment is either a run of mrkdwn prose or a Markdown table's cells, header row first.
type segment struct {
	text  string
	table [][]string
}

// toMrkdwn rewrites common GitHub-flavored Markdown into Slack mrkdwn, rendering tables as code blocks.
func toMrkdwn(text string) string {
	segs := parseMarkdown(text)
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.text
		if s.table != nil {
			parts[i] = codeBlock(s.table)
		}
	}
	return strings.Join(parts, "\n")
}

// parseMarkdown splits text into prose converted to mrkdwn and tables, leaving fenced code untouched.
func parseMarkdown(text string) []segment {
	lines := strings.Split(text, "\n")
	var segs []segment
	var prose []string
	flush := func() {
		if prose != nil {
			segs = append(segs, segment{text: strings.Join(prose, "\n")})
			prose = nil
		}
	}
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case isFence(line):
			inFence = !inFence
			prose = append(prose, line)
		case inFence:
			prose = append(prose, line)
		case tableLen(lines[i:]) > 0:
			n := tableLen(lines[i:])
			flush()
			segs = append(segs, segment{table: tableRows(lines[i : i+n])})
			i += n - 1
		default:
			prose = append(prose, inlineMrkdwn(line))
		}
	}
	flush()
	return segs
}

func isFence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), codeFence)
}

func inlineMrkdwn(line string) string {
	line = mdBoldStars.ReplaceAllString(line, "*$1*")
	line = mdBoldUnder.ReplaceAllString(line, "*$1*")
	line = mdLink.ReplaceAllString(line, "$1 ($2)")
	line = mdHeading.ReplaceAllString(line, "*$1*")
	return mdBullet.ReplaceAllString(line, "$1• ")
}

// tableLen returns how many lines starting at lines[0] form a Markdown table, or 0 if none does.
func tableLen(lines []string) int {
	if len(lines) < 2 || !strings.Contains(lines[0], "|") ||
		!strings.Contains(lines[1], "|") || !mdTableSep.MatchString(lines[1]) {
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
	if !strings.Contains(line, "|") || isFence(line) || framed && !strings.HasPrefix(line, "|") {
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
		cells[i] = strings.TrimSpace(mdCellMarks.ReplaceAllString(cell, ""))
	}
	return cells
}

// codeBlock renders rows as space-aligned columns in a code block, which Slack shows in monospace.
func codeBlock(rows [][]string) string {
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
	out = append(out, codeFence)
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
	return strings.Join(append(out, codeFence), "\n")
}
