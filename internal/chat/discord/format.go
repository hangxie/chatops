package discord

import (
	"strings"
	"unicode/utf8"

	"github.com/hangxie/chatops/internal/chat/markdown"
	"github.com/hangxie/chatops/internal/conversation"
)

const (
	// maxMessageRunes is Discord's message content limit.
	maxMessageRunes = 2000
	// maxMessages bounds the messages one answer is split into; the footer may add one more.
	maxMessages    = 5
	maxFooterRunes = 1000
	truncatedNote  = "\n[reply truncated]"
	subtextPrefix  = "-# "
	// maxOpenerRunes caps a reopened fence's line; a longer one is reopened as a bare fence.
	maxOpenerRunes = 16
)

// messages renders a reply as Discord message contents: the answer split at line breaks, then the footer as subtext.
func messages(reply conversation.Reply) []string {
	noteRunes := utf8.RuneCountInString(truncatedNote)
	out := chunk(markdown.Render(reply.Text, nil), maxMessageRunes-noteRunes)
	if len(out) > maxMessages {
		out = out[:maxMessages]
		out[maxMessages-1] += truncatedNote
	}
	footer := subtext(reply.Footer)
	if footer == "" {
		return out
	}
	if n := len(out); n > 0 && utf8.RuneCountInString(out[n-1])+len("\n\n")+utf8.RuneCountInString(footer) <= maxMessageRunes {
		out[n-1] += "\n\n" + footer
		return out
	}
	return append(out, footer)
}

// subtext prefixes each footer line with "-# ", which Discord shows as smaller grey text, within maxFooterRunes.
func subtext(footer string) string {
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(footer), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, subtextPrefix+line)
		}
	}
	text := strings.Join(lines, "\n")
	if runes := []rune(text); len(runes) > maxFooterRunes {
		text = string(runes[:maxFooterRunes-1]) + "…"
	}
	return text
}

// chunk splits text at line breaks into non-blank pieces of at most limit runes, closing and reopening a code
// fence that spans pieces so each renders on its own.
func chunk(text string, limit int) []string {
	c := chunker{limit: limit}
	for line := range strings.SplitSeq(text, "\n") {
		c.add(line)
	}
	c.emit()
	return c.chunks
}

type chunker struct {
	limit        int
	chunks, cur  []string
	runes, lines int
	opener       string // the open code fence's line, reused to reopen it; "" outside a fence
}

func (c *chunker) add(line string) {
	opener := c.opener
	switch {
	case opener == "" && markdown.IsFence(line):
		opener = strings.TrimSpace(line)
		if utf8.RuneCountInString(opener) > maxOpenerRunes {
			opener = markdown.Fence
		}
	case markdown.IsFence(line):
		opener = ""
	}
	// Inside a fence, keep room for this chunk's closing fence and the next chunk's reopened one.
	room, reserve := c.limit, 0
	if opener != "" {
		reserve = len(markdown.Fence) + 1
		room -= reserve + utf8.RuneCountInString(opener) + 1
	}
	for _, piece := range splitRunes(line, max(room, 1)) {
		if c.lines > 0 && c.runes+c.size(piece)+reserve > c.limit {
			c.emit()
		}
		c.runes += c.size(piece)
		c.cur = append(c.cur, piece)
		c.lines++
	}
	c.opener = opener
}

func (c *chunker) size(piece string) int {
	if len(c.cur) == 0 {
		return utf8.RuneCountInString(piece)
	}
	return utf8.RuneCountInString(piece) + 1
}

func (c *chunker) emit() {
	if c.lines == 0 {
		return
	}
	if c.opener != "" {
		c.cur = append(c.cur, markdown.Fence)
	}
	if s := strings.Trim(strings.Join(c.cur, "\n"), "\n"); strings.TrimSpace(s) != "" {
		c.chunks = append(c.chunks, s)
	}
	c.cur, c.runes, c.lines = nil, 0, 0
	if c.opener != "" {
		c.cur, c.runes = []string{c.opener}, utf8.RuneCountInString(c.opener)
	}
}

// splitRunes cuts line into pieces of at most limit runes.
func splitRunes(line string, limit int) []string {
	runes := []rune(line)
	if len(runes) <= limit {
		return []string{line}
	}
	var pieces []string
	for len(runes) > limit {
		pieces = append(pieces, string(runes[:limit]))
		runes = runes[limit:]
	}
	return append(pieces, string(runes))
}
