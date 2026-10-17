package slack

import (
	"strings"
	"unicode/utf8"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackutilsx"

	"github.com/hangxie/chatops/internal/chat/markdown"
	"github.com/hangxie/chatops/internal/conversation"
)

// Slack Block Kit limits for a message and its table, section, and context blocks.
const (
	maxBlocks          = 50
	maxTableRows       = 100
	maxTableCols       = 20
	maxTableChars      = 10000
	maxSectionChars    = 3000
	maxContextElements = 10
)

// toBlocks renders a reply as Block Kit with one native table and the footer below a divider; nil means plain text.
func toBlocks(reply conversation.Reply) []slackapi.Block {
	blocks, native := bodyBlocks(reply.Text)
	if reply.Footer == "" {
		if !native {
			return nil
		}
		return capBlocks(blocks, maxBlocks)
	}
	blocks = capBlocks(blocks, maxBlocks-2)
	return append(blocks, slackapi.NewDividerBlock(), footerBlock(reply.Footer))
}

// bodyBlocks renders text as section blocks around at most one native table, reporting whether it has one.
func bodyBlocks(text string) ([]slackapi.Block, bool) {
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
	for _, s := range markdown.Parse(text, inlineMrkdwn) {
		switch {
		case s.Table == nil:
			prose = append(prose, s.Text)
		case !native && nativeTable(s.Table):
			flush()
			blocks = append(blocks, tableBlock(s.Table))
			native = true
		default:
			prose = append(prose, markdown.CodeBlock(s.Table))
		}
	}
	flush()
	return blocks, native
}

// footerBlock renders the footer as a context block, which Slack shows in smaller grey text.
func footerBlock(footer string) *slackapi.ContextBlock {
	chunks := chunkEscaped(footer, maxSectionChars)
	if len(chunks) > maxContextElements {
		chunks = append(chunks[:maxContextElements-1], "[footer truncated]")
	}
	elements := make([]slackapi.MixedElement, len(chunks))
	for i, chunk := range chunks {
		elements[i] = slackapi.NewTextBlockObject(slackapi.MarkdownType, chunk, false, false)
	}
	return slackapi.NewContextBlock("", elements...)
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
	const fenceRunes = len(markdown.Fence) + 1
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
			add(markdown.Fence)
		}
		if s := strings.Trim(strings.Join(cur, "\n"), "\n"); strings.TrimSpace(s) != "" {
			chunks = append(chunks, s)
		}
		cur, n, lines = nil, 0, 0
		if inFence {
			add(markdown.Fence)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		fenced := inFence != markdown.IsFence(line)
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
