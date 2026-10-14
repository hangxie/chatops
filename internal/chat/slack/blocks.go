package slack

import (
	"strings"
	"unicode/utf8"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackutilsx"
)

// Slack Block Kit limits for a message and its table and section blocks.
const (
	maxBlocks       = 50
	maxTableRows    = 100
	maxTableCols    = 20
	maxTableChars   = 10000
	maxSectionChars = 3000
)

// toBlocks renders text as Block Kit with the first fitting Markdown table as a native table block, since
// Slack allows one table per message; it returns nil when no table qualifies so the reply stays plain text.
func toBlocks(text string) []slackapi.Block {
	var blocks []slackapi.Block
	var prose []string
	flush := func() {
		for _, chunk := range chunkEscaped(strings.Join(prose, "\n"), maxSectionChars) {
			blocks = append(blocks, slackapi.NewSectionBlock(
				slackapi.NewTextBlockObject(slackapi.MarkdownType, chunk, false, false), nil, nil,
			))
		}
		prose = nil
	}
	native := false
	for _, s := range parseMarkdown(text) {
		switch {
		case s.table == nil:
			prose = append(prose, s.text)
		case !native && nativeTable(s.table):
			flush()
			blocks = append(blocks, tableBlock(s.table))
			native = true
		default:
			prose = append(prose, codeBlock(s.table))
		}
	}
	if !native {
		return nil
	}
	flush()
	return capBlocks(blocks, maxBlocks)
}

// capBlocks trims blocks to limit, keeping the table and ending with a truncation notice.
func capBlocks(blocks []slackapi.Block, limit int) []slackapi.Block {
	if len(blocks) <= limit {
		return blocks
	}
	kept := blocks[: limit-1 : limit-1]
	for i, b := range blocks {
		if b.BlockType() == slackapi.MBTTable && i >= limit-1 {
			kept = append(blocks[:limit-2:limit-2], b)
		}
	}
	notice := slackapi.NewTextBlockObject(slackapi.MarkdownType, "[reply truncated]", false, false)
	return append(kept, slackapi.NewSectionBlock(notice, nil, nil))
}

// nativeTable reports whether rows fit a Slack table block, which needs a full grid of non-empty cells.
func nativeTable(rows [][]string) bool {
	if len(rows) > maxTableRows || len(rows[0]) > maxTableCols {
		return false
	}
	chars := 0
	for _, cells := range rows {
		if len(cells) != len(rows[0]) {
			return false
		}
		for _, cell := range cells {
			if cell == "" {
				return false
			}
			chars += utf8.RuneCountInString(cell)
		}
	}
	return chars <= maxTableChars
}

func tableBlock(rows [][]string) *slackapi.TableBlock {
	table := slackapi.NewTableBlock("")
	for _, cells := range rows {
		row := make([]slackapi.TableCell, len(cells))
		for i, cell := range cells {
			row[i] = slackapi.NewTableRawTextCell(cell)
		}
		table.AddRow(row...)
	}
	return table
}

// chunkEscaped escapes text for mrkdwn and splits it at line breaks into non-blank chunks of at most limit
// runes, closing and reopening a code fence that spans chunks so each section renders on its own.
func chunkEscaped(text string, limit int) []string {
	const fenceRunes = len(codeFence) + 1
	var chunks, cur []string
	n, lines, inFence := 0, 0, false
	add := func(s string) {
		if len(cur) > 0 {
			n++
		}
		cur, n = append(cur, s), n+utf8.RuneCountInString(s)
	}
	fits := func(size int, fenced bool) bool {
		if len(cur) > 0 {
			size++
		}
		if fenced {
			size += fenceRunes
		}
		return n+size <= limit
	}
	flush := func() {
		if lines == 0 {
			return
		}
		if inFence {
			add(codeFence)
		}
		if s := strings.Trim(strings.Join(cur, "\n"), "\n"); strings.TrimSpace(s) != "" {
			chunks = append(chunks, s)
		}
		cur, n, lines = nil, 0, 0
		if inFence {
			add(codeFence)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		fenced := inFence != isFence(line)
		room := limit
		if fenced {
			room -= 2 * fenceRunes
		}
		for i, piece := range splitEscaped(line, room) {
			if i > 0 || !fits(utf8.RuneCountInString(piece), fenced) {
				flush()
			}
			add(piece)
			lines++
		}
		inFence = fenced
	}
	flush()
	return chunks
}

// splitEscaped escapes line and cuts it into pieces of at most limit runes without splitting an entity.
func splitEscaped(line string, limit int) []string {
	var pieces []string
	var cur strings.Builder
	n := 0
	for _, r := range line {
		e := slackutilsx.EscapeMessage(string(r))
		if size := utf8.RuneCountInString(e); n+size > limit {
			pieces = append(pieces, cur.String())
			cur.Reset()
			n = 0
		}
		cur.WriteString(e)
		n += utf8.RuneCountInString(e)
	}
	return append(pieces, cur.String())
}
